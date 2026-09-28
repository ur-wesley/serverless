package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"actions/internal/artifacts"
	"actions/internal/bus"
	"actions/internal/deploy"
	"actions/internal/functions"
	"actions/internal/gateway"
	"actions/internal/runner"
	"actions/internal/scheduler"
	"actions/internal/sidecar"
	"actions/internal/store"
)

func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("incoming request", "method", r.Method, "path", r.URL.Path, "host", r.Host, "remote", r.RemoteAddr)
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic recovered", "err", v, "path", r.URL.Path)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	port := getenv("PORT", "8080")

	sqlitePath := getenv("SQLITE_PATH", filepath.Join("data", "actions.db"))
	if dir := filepath.Dir(sqlitePath); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	st, err := store.Open(sqlitePath)
	if err != nil {
		slog.Error("open sqlite", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	tokens := sidecar.NewRegistry()
	arts := artifacts.NewFromEnv()
	backend := runner.NewDockerRunner(runner.Config{
		Network:    getenv("RUNNER_NETWORK", "actions-net"),
		SandboxNet: os.Getenv("RUNNER_SANDBOX_NET"), // set in compose; unset = dev mode
		HostGW:     getenv("RUNNER_HOST_GW", "127.0.0.1"),
		Tokens:     tokens,
		Arts:       arts,
	})
	defer backend.Close()

	svc := &deploy.Service{Store: st, Artifacts: arts}

	qbus := bus.NewFromEnv()
	defer qbus.Close()

	kv, kvClose := sidecar.NewKVFromEnv()
	defer kvClose()
	logs := sidecar.NewLogRing()
	sidecarURL := functions.SidecarURLFromEnv(port)
	// Blob transfer URLs must be dialable from handler containers.
	sc := &sidecar.Server{KV: kv, Blobs: sidecar.NewBlobFromEnv(arts, sidecarURL), Bus: qbus, Logs: logs, Tokens: tokens}

	helloImage := getenv("HELLO_IMAGE", "hello-ts:latest")
	lookup := func(name string) (runner.FunctionRef, bool) {
		return functions.Resolve(st, name, helloImage, sidecarURL)
	}

	sched := scheduler.New(st, backend, qbus, helloImage, sidecarURL)
	sched.Start()
	defer sched.Stop()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/f/", gateway.NewHandler(backend, lookup))
	// Operator queue ingress: POST /pub/:topic with raw body -> bus ->
	// subscriber functions. (Handlers publish via sidecar with tokens.)
	mux.HandleFunc("POST /pub/", func(w http.ResponseWriter, r *http.Request) {
		topic := strings.TrimPrefix(r.URL.Path, "/pub/")
		if topic == "" || strings.Contains(topic, "/") {
			http.Error(w, "topic required", http.StatusBadRequest)
			return
		}
		msg, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if err := qbus.Publish(r.Context(), topic, msg); err != nil {
			http.Error(w, "publish: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	sc.RegisterRoutes(mux)
	sidecar.RegisterTransferRoutes(mux, arts, tokens)
	mux.HandleFunc("POST /deploy", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name       string `json:"name"`
			ConfigTOML string `json:"config_toml"`
			SrcZipB64  string `json:"src_zip_b64"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&in); err != nil {
			http.Error(w, "bad JSON (max 32MB)", http.StatusBadRequest)
			return
		}
		zipBytes, err := base64.StdEncoding.DecodeString(in.SrcZipB64)
		if err != nil || len(zipBytes) == 0 {
			http.Error(w, "src_zip_b64 must be non-empty base64", http.StatusBadRequest)
			return
		}
		res, err := svc.Deploy(r.Context(), deploy.Request{Name: in.Name, ConfigTOML: in.ConfigTOML, SrcZip: zipBytes})
		if err != nil {
			slog.Warn("deploy failed", "fn", in.Name, "err", err)
			http.Error(w, "deploy: "+err.Error(), http.StatusBadRequest)
			return
		}
		sched.Sync() // pick up new cron/queue triggers immediately
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})
	mux.HandleFunc("GET /functions", func(w http.ResponseWriter, r *http.Request) {
		fns, err := st.ListFunctions()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if fns == nil {
			fns = []store.Function{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fns)
	})
	mux.HandleFunc("GET /logs", func(w http.ResponseWriter, r *http.Request) {
		fn := r.URL.Query().Get("fn")
		if fn == "" {
			http.Error(w, "?fn= required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(logs.Tail(fn, 100))
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withRecovery(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      120 * time.Second, // cold start + max 60s invoke
		IdleTimeout:       120 * time.Second,
	}
	slog.Info("controlplane listening", "port", port, "sqlite", sqlitePath, "sidecar", sidecarURL,
		"sandbox", os.Getenv("RUNNER_SANDBOX_NET") != "")
	logSelfInspect()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

// logSelfInspect shells out to `docker inspect` on our own container (docker.sock
// is mounted) and logs labels + networks. Temporary diagnostic for Traefik routing.
func logSelfInspect() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hn, _ := os.Hostname()
	out, err := exec.CommandContext(ctx, "docker", "inspect", hn, "--format",
		"{{range $k,$v := .NetworkSettings.Networks}}[{{$k}}]{{end}} || {{range $k,$v := .Config.Labels}}{{$k}}={{$v}} ||| {{end}}").CombinedOutput()
	if err != nil {
		slog.Warn("self-inspect failed", "err", err, "out", strings.TrimSpace(string(out)))
	} else {
		emitChunked("self-inspect-chunk", hn, strings.TrimSpace(string(out)))
	}
	// Find traefik container(s) and dump recent relevant log lines.
	ps, err := exec.CommandContext(ctx, "docker", "ps", "--format", "{{.Names}}|{{.Image}}").CombinedOutput()
	if err != nil {
		slog.Warn("docker ps failed", "err", err)
		return
	}
	emitChunked("docker-ps", hn, strings.TrimSpace(string(ps)))
	if out, err := exec.CommandContext(ctx, "docker", "version", "--format",
		"client={{.Client.Version}}/{{.Client.ApiVersion}} server={{.Server.Version}}/{{.Server.ApiVersion}} min={{.Server.MinAPIVersion}}").CombinedOutput(); err != nil {
		slog.Warn("docker version failed", "err", err)
	} else {
		slog.Info("docker-version", "detail", strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(ps)), "\n") {
		name := strings.SplitN(line, "|", 2)[0]
		if !strings.Contains(strings.ToLower(line), "traefik") {
			continue
		}
		if strings.Contains(name, "cert-sync") || !strings.Contains(name, "dokploy-traefik") {
			continue
		}
		env, err := exec.CommandContext(ctx, "docker", "inspect", name, "--format", "{{json .Config.Env}}").CombinedOutput()
		if err != nil {
			slog.Warn("traefik inspect failed", "container", name, "err", err)
		} else {
			emitChunked("traefik-env-"+name, hn, strings.TrimSpace(string(env)))
		}
		lg, err := exec.CommandContext(ctx, "docker", "logs", "--tail", "2000", name).CombinedOutput()
		if err != nil {
			slog.Warn("traefik logs failed", "container", name, "err", err)
			continue
		}
		var firstErr, lastErr string
		n := 0
		for _, l := range strings.Split(string(lg), "\n") {
			if strings.Contains(l, "ERR") || strings.Contains(strings.ToLower(l), "level=error") {
				n++
				clean := stripANSI(l)
				if len(clean) > 300 {
					clean = clean[:300]
				}
				if firstErr == "" {
					firstErr = clean
				}
				lastErr = clean
			}
		}
		slog.Info("traefik-err-summary", "container", name, "errLines", n, "first", firstErr, "last", lastErr)
		// Try traefik API for router table (usually disabled; informative either way).
		if api, err := exec.CommandContext(ctx, "wget", "-qO-", "--timeout=5",
			"http://"+name+":8080/api/http/routers").CombinedOutput(); err != nil {
			slog.Info("traefik-api", "container", name, "result", "unreachable: "+strings.TrimSpace(string(api)))
		} else {
			s := string(api)
			hasOurs := strings.Contains(s, "svr.w4y.io") || strings.Contains(s, "serverless")
			emitChunked("traefik-api-routers", hn, "hasOurs="+boolStr(hasOurs)+" len="+itoa(len(s))+" "+s)
		}
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		if r == 0x1b {
			esc = true
			continue
		}
		if esc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				esc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func itoa(n int) string { return strconv.Itoa(n) }

func emitChunked(msg, hn, s string) {
	if s == "" {
		slog.Info(msg, "hostname", hn, "detail", "(empty)")
		return
	}
	for len(s) > 0 {
		chunk := s
		if len(chunk) > 2500 {
			chunk = chunk[:2500]
		}
		slog.Info(msg, "hostname", hn, "detail", chunk)
		s = s[len(chunk):]
	}
}
