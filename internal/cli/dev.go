package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ur-wesley/oort/internal/deploy"
	"github.com/ur-wesley/oort/internal/devmock"
)

// DevOptions controls the local dev loop.
type DevOptions struct {
	Dir     string
	URL     string
	Offline bool
	CmdArgs []string
	// Port is the stable dev gateway port users open in a browser.
	// 0 = pick a free port. Default 3000 (CLI flag).
	Port int
	// Watch restarts the handler when source files change.
	Watch bool
	// OnReady fires with the bound gateway URL once the listener is up.
	// Used by automated tests (Port 0 picks a free port); CLI leaves it nil.
	OnReady func(devURL string)
}

// defaultHandlerCmd guesses how to run the function locally.
func defaultHandlerCmd(runtime, dir string, extra []string) []string {
	if len(extra) > 0 {
		return extra
	}
	switch runtime {
	case "ts":
		return []string{"bun", "src/index.ts"}
	case "rust":
		return []string{"cargo", "run", "--quiet"}
	default:
		return []string{"go", "run", "."}
	}
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	return port, err
}

// Dev runs the handler locally with PORT + ACTIONS_SIDECAR_URL set.
// offline=true starts an in-memory sidecar mock; otherwise the sidecar
// URL points at the control plane (url) so KV/Blob/Queue hit real backends.
//
// Kept for compatibility: defaults to gateway on :3000 without watching.
// The CLI calls DevWithOptions with --port/--watch flags.
func Dev(ctx context.Context, dir, url string, offline bool, cmdArgs []string) error {
	return DevWithOptions(ctx, DevOptions{
		Dir: dir, URL: url, Offline: offline, CmdArgs: cmdArgs,
		Port: 3000, Watch: false,
	})
}

// DevWithOptions runs handler + stable dev gateway (bun-run-dev equivalent):
//
//	handler (ephemeral 127.0.0.1:*) <- dev gateway (127.0.0.1:Port) <- browser/curl
//
// The gateway translates plain HTTP (ANY /..., /f/<name>/...) into
// POST /invoke protojson so any route is a working local URL, and --watch
// restarts the handler on file change without dropping the gateway.
func DevWithOptions(ctx context.Context, opts DevOptions) error {
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}
	tomlRaw, err := os.ReadFile(filepath.Join(dir, "actions.toml"))
	if err != nil {
		return fmt.Errorf("read actions.toml: %w", err)
	}
	cfg, err := deploy.ParseConfig(string(tomlRaw))
	if err != nil {
		return err
	}
	sidecarURL := opts.URL
	if opts.Offline {
		m := devmock.New()
		mockURL, close, err := m.Listen()
		if err != nil {
			return fmt.Errorf("mock sidecar: %w", err)
		}
		defer close()
		sidecarURL = mockURL
		fmt.Printf("mock sidecar at %s\n", mockURL)
	}
	timeoutMs := cfg.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = 10000
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sup := &handlerSup{
		dir:        dir,
		argv:       defaultHandlerCmd(cfg.Runtime, dir, opts.CmdArgs),
		sidecarURL: sidecarURL,
	}
	if err := sup.restart(); err != nil {
		return err
	}
	defer sup.stop()
	if err := sup.waitHealthy(20 * time.Second); err != nil {
		fmt.Printf("warning: handler not healthy yet (%v) — gateway stays up, retrying in background\n", err)
	}

	gw := &devGateway{
		fnName:     cfg.Name,
		route:      cfg.Route,
		sidecarURL: sidecarURL,
		timeoutMs:  timeoutMs,
	}
	gw.handlerAddr.Store(sup.addr())

	listenAddr := fmt.Sprintf("127.0.0.1:%d", opts.Port)
	if opts.Port == 0 {
		listenAddr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		if opts.Port != 0 {
			return fmt.Errorf("dev gateway: port %d in use (try --port 0 or another --port): %w", opts.Port, err)
		}
		return fmt.Errorf("dev gateway listen: %w", err)
	}
	defer ln.Close()
	devURL := "http://" + ln.Addr().String()
	invokePath := "/" + strings.Trim(devEventPath("/f/"+cfg.Name+"/", cfg.Name), "/")
	if invokePath == "/" || invokePath == "" {
		invokePath = "/"
	}
	fmt.Printf("%s dev on %s (open %s%s)\n", cfg.Name, devURL, devURL, invokePath)
	fmt.Printf("  handler %s (internal)  sidecar %s\n", sup.addr(), sidecarURL)
	if opts.Watch {
		fmt.Printf("  watch on — editing src restarts the handler, gateway stays on %s\n", devURL)
	} else {
		fmt.Printf("  tip: oort dev --watch --port %d for auto-reload\n", opts.Port)
	}

	srv := &http.Server{Handler: gw, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }() //nolint:errcheck
	defer srv.Close()
	if opts.OnReady != nil {
		opts.OnReady(devURL)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	var watchErr chan error
	if opts.Watch {
		watchErr = make(chan error, 1)
		go func() {
			watchErr <- watchDir(ctx, dir, 500*time.Millisecond, func() {
				fmt.Printf("change detected — restarting handler...\n")
				if err := sup.restart(); err != nil {
					fmt.Printf("restart failed: %v\n", err)
					return
				}
				gw.handlerAddr.Store(sup.addr())
				if err := sup.waitHealthy(15 * time.Second); err != nil {
					fmt.Printf("handler not healthy yet: %v\n", err)
					return
				}
				fmt.Printf("handler restarted on %s\n", sup.addr())
			})
		}()
	}

	done := make(chan error, 1)
	go func() {
		// Follow handler replacements across watch restarts: only a
		// genuine exit (same generation) is reported; restarts and
		// intentional stops just move the waiter along.
		for {
			p, g := sup.current()
			if p == nil {
				return // intentionally stopped
			}
			err := p.Wait()
			_, g2 := sup.current()
			if g2 != g {
				continue // replaced by restart; wait on the new process
			}
			done <- err
			return
		}
	}()

	select {
	case err := <-done:
		sup.stop()
		return err
	case err := <-watchErr:
		return err
	case <-sig:
		cancel()
		_ = srv.Close()
		sup.stop()
		return context.Canceled
	case <-ctx.Done():
		_ = srv.Close()
		sup.stop()
		return ctx.Err()
	}
}

// handlerSup owns one handler process; restart() kills the old one and
// starts a new one on a fresh ephemeral port. proc/gen are mutex-guarded
// so the exit waiter, the watcher, and shutdown can't race or follow a
// stale process after a restart.
type handlerSup struct {
	dir        string
	argv       []string
	sidecarURL string

	mu    sync.Mutex
	proc  *exec.Cmd
	gen   int          // bumped on every restart/stop; lets the waiter detect replacement
	addr_ atomic.Value // string like 127.0.0.1:PORT
}

func (s *handlerSup) addr() string {
	v, _ := s.addr_.Load().(string)
	return v
}

// current returns the live process and its generation.
func (s *handlerSup) current() (*exec.Cmd, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proc, s.gen
}

func (s *handlerSup) restart() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	port, err := freePort()
	if err != nil {
		return err
	}
	addr := "127.0.0.1:" + port
	argv := s.argv
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), "PORT="+port, "ACTIONS_SIDECAR_URL="+s.sidecarURL)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	startDetached(cmd) // own process group so stop() kills the whole tree (go run -> handler)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %v: %w", argv, err)
	}
	s.proc = cmd
	s.gen++
	s.addr_.Store(addr)
	return nil
}

// stop kills the handler tree and clears it. Callers must hold s.mu;
// use stop() otherwise.
func (s *handlerSup) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	s.gen++
}

func (s *handlerSup) stopLocked() {
	if s.proc != nil && s.proc.Process != nil {
		_ = killTree(s.proc) // kills children too; plain Kill orphans `go run` handlers
		_, _ = s.proc.Process.Wait()
		s.proc = nil
	}
}

func (s *handlerSup) waitHealthy(timeout time.Duration) error {
	addr := s.addr()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		if p, _ := s.current(); p == nil {
			return fmt.Errorf("handler exited during startup")
		}
		resp, err := client.Get("http://" + addr + "/healthz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("handler at %s not ready after %s", addr, timeout)
}

// devGateway translates plain browser HTTP into POST /invoke ABI calls.
type devGateway struct {
	fnName      string
	route       string
	sidecarURL  string
	timeoutMs   int
	handlerAddr atomic.Value // string
}

func (g *devGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Dev info page.
	if r.URL.Path == "/" && (r.Method == "GET" || r.Method == "HEAD") {
		g.serveIndex(w, r)
		return
	}
	addrVal := g.handlerAddr.Load()
	if addrVal == nil {
		http.Error(w, "handler restarting", http.StatusBadGateway)
		return
	}
	handlerBase := "http://" + addrVal.(string)

	// Raw ABI passthroughs.
	if r.URL.Path == "/healthz" {
		proxyRaw(w, r, handlerBase+"/healthz", 4<<20)
		return
	}
	if r.URL.Path == "/invoke" {
		proxyRaw(w, r, handlerBase+"/invoke", 8<<20)
		return
	}

	eventPath := devEventPath(r.URL.Path, g.fnName)
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	headers := map[string]string{}
	for k := range r.Header {
		lk := strings.ToLower(k)
		headers[lk] = r.Header.Get(k)
	}
	query := map[string]string{}
	for k := range r.URL.Query() {
		query[k] = r.URL.Query().Get(k)
	}
	payload, err := buildInvokeRequest(r.Method, eventPath, headers, query, body,
		g.fnName, g.sidecarURL, g.timeoutMs)
	if err != nil {
		http.Error(w, "encode invoke", http.StatusInternalServerError)
		return
	}
	client := &http.Client{Timeout: time.Duration(g.timeoutMs+5000) * time.Millisecond}
	resp, err := client.Post(handlerBase+"/invoke", "application/json", bytes.NewReader(payload))
	if err != nil {
		http.Error(w, "handler not ready (restarting?): "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		http.Error(w, "read handler response", http.StatusBadGateway)
		return
	}
	status, outHeaders, outBody, err := parseInvokeResponse(raw)
	if err != nil {
		http.Error(w, "handler returned bad invoke JSON: "+err.Error(), http.StatusBadGateway)
		return
	}
	for k, v := range outHeaders {
		w.Header().Set(k, v)
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(outBody)
}

func (g *devGateway) serveIndex(w http.ResponseWriter, r *http.Request) {
	base := "http://" + r.Host
	tryPath := "/"
	if g.route != "" {
		tryPath = g.route
	} else {
		tryPath = "/f/" + g.fnName + "/"
	}
	w.Header().Set("content-type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><body style="font-family:system-ui;padding:2em">`+
		`<h1>%s — oort dev</h1>`+
		`<p>Try: <a href="%s">%s%s</a> · <a href="/healthz">/healthz</a></p>`+
		`<p style="color:#666">Any path becomes event.path via POST /invoke; sidecar %s</p>`+
		`</body></html>`,
		g.fnName, base+tryPath, base, tryPath, g.sidecarURL)
}

// devEventPath maps a browser path to the ABI event.path.
// /f/<name>/rest (or /f/other/rest for the single local fn) -> /rest,
// everything else passes through unchanged.
func devEventPath(urlPath, fnName string) string {
	if strings.HasPrefix(urlPath, "/f/") {
		rest := strings.TrimPrefix(urlPath, "/f/")
		if i := strings.Index(rest, "/"); i >= 0 {
			return "/" + strings.Trim(rest[i+1:], "/")
		}
		return "/"
	}
	if urlPath == "" {
		return "/"
	}
	return urlPath
}

type devHTTPEvent struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Query   map[string]string `json:"query"`
	Body    string            `json:"body"`
}

type devInvokeCtx struct {
	FunctionName string `json:"function_name"`
	Version      string `json:"version"`
	RequestID    string `json:"request_id"`
	DeadlineMs   int64  `json:"deadline_ms"`
	SidecarURL   string `json:"sidecar_url,omitempty"`
}

type devInvokeRequest struct {
	Event devHTTPEvent `json:"event"`
	Ctx   devInvokeCtx `json:"ctx"`
}

type devInvokeResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

func buildInvokeRequest(method, eventPath string, headers, query map[string]string, body []byte, fnName, sidecarURL string, timeoutMs int) ([]byte, error) {
	if headers == nil {
		headers = map[string]string{}
	}
	if query == nil {
		query = map[string]string{}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	req := devInvokeRequest{
		Event: devHTTPEvent{
			Method: method, Path: eventPath, Headers: headers, Query: query,
			Body: base64.StdEncoding.EncodeToString(body),
		},
		Ctx: devInvokeCtx{
			FunctionName: fnName, Version: "dev",
			RequestID:  hex.EncodeToString(b[:]),
			DeadlineMs: time.Now().Add(time.Duration(timeoutMs) * time.Millisecond).UnixMilli(),
			SidecarURL: sidecarURL,
		},
	}
	return json.Marshal(req)
}

func parseInvokeResponse(raw []byte) (int, map[string]string, []byte, error) {
	var resp devInvokeResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, nil, nil, err
	}
	body, err := base64.StdEncoding.DecodeString(resp.Body)
	if err != nil {
		// Empty body is fine; only fail on non-empty garbage.
		if strings.TrimSpace(resp.Body) == "" {
			return resp.Status, resp.Headers, nil, nil
		}
		return 0, nil, nil, fmt.Errorf("body not base64: %w", err)
	}
	return resp.Status, resp.Headers, body, nil
}

func proxyRaw(w http.ResponseWriter, r *http.Request, target string, limit int64) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, limit))
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "proxy", http.StatusBadGateway)
		return
	}
	for k, vv := range r.Header {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "handler not ready: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, limit))
}

// watchDir polls dir for relevant source changes until ctx ends,
// calling onChange (debounced) on each change set.
func watchDir(ctx context.Context, dir string, interval time.Duration, onChange func()) error {
	prev, err := snapshotDir(dir)
	if err != nil {
		return err
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	var pending time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-t.C:
			cur, err := snapshotDir(dir)
			if err != nil {
				continue
			}
			if !snapshotsEqual(prev, cur) {
				prev = cur
				pending = now
			} else if !pending.IsZero() && now.Sub(pending) >= interval {
				pending = time.Time{}
				onChange()
			}
		}
	}
}

func snapshotDir(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // ignore unreadable entries
		}
		name := d.Name()
		if d.IsDir() {
			if name == "node_modules" || name == ".git" || name == ".beads" || name == "dist" || name == "target" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".ts", ".js", ".mjs", ".cjs", ".json", ".go", ".mod", ".sum", ".toml", ".rs", ".lock":
		default:
			return nil
		}
		if strings.HasSuffix(name, ".exe") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			rel = p
		}
		out[rel] = fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	return out, err
}

func snapshotsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}
