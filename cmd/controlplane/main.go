package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ur-wesley/serverless/internal/artifacts"
	"github.com/ur-wesley/serverless/internal/auth"
	"github.com/ur-wesley/serverless/internal/bus"
	"github.com/ur-wesley/serverless/internal/controlauth"
	"github.com/ur-wesley/serverless/internal/deploy"
	"github.com/ur-wesley/serverless/internal/functions"
	"github.com/ur-wesley/serverless/internal/gateway"
	"github.com/ur-wesley/serverless/internal/runner"
	"github.com/ur-wesley/serverless/internal/scheduler"
	"github.com/ur-wesley/serverless/internal/sidecar"
	"github.com/ur-wesley/serverless/internal/store"
)

func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			slog.Info("incoming request", "method", r.Method, "path", r.URL.Path, "host", r.Host, "remote", r.RemoteAddr)
		}
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

// sidecarInvoker adapts runner.Backend to the sidecar intercom Invoke API.
type sidecarInvoker struct {
	backend    runner.Backend
	store      *store.Store
	sidecarURL string
}

func (s *sidecarInvoker) Invoke(ctx context.Context, target, method, path string, headers, query map[string]string, body []byte) (int, map[string]string, []byte, error) {
	info, ok := functions.ResolveFull(s.store, target, s.sidecarURL)
	if !ok {
		return 0, nil, nil, &sidecarInvokeError{msg: "unknown function"}
	}
	if headers == nil {
		headers = map[string]string{}
	}
	if query == nil {
		query = map[string]string{}
	}
	payload, err := json.Marshal(map[string]any{
		"event": map[string]any{
			"method": method, "path": path, "headers": headers,
			"query": query, "body": base64.StdEncoding.EncodeToString(body),
		},
		"ctx": map[string]any{
			"function_name": info.Ref.Name, "version": info.Ref.Version,
			"request_id": newRequestID(), "deadline_ms": time.Now().Add(runner.ClampTimeout(info.Ref.Timeout)).UnixMilli(),
			"sidecar_url": info.Ref.SidecarURL,
		},
	})
	if err != nil {
		return 0, nil, nil, err
	}
	resp, err := s.backend.Invoke(ctx, info.Ref, payload)
	if err != nil {
		return 0, nil, nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(resp.Body)
	if err != nil {
		return 0, nil, nil, &sidecarInvokeError{msg: "bad handler body"}
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	if resp.Headers == nil {
		resp.Headers = map[string]string{}
	}
	return status, resp.Headers, raw, nil
}

type sidecarInvokeError struct{ msg string }

func (e *sidecarInvokeError) Error() string { return e.msg }

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
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
	if s3store, ok := arts.(*artifacts.S3); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := s3store.EnsureBucket(ctx); err != nil {
			slog.Warn("artifacts bucket not ensured (deploys/blobs will fail until RustFS is reachable)", "err", err)
		}
		cancel()
	}
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
	sc.OwnerOf = func(name string) (string, bool) {
		fn, err := st.GetFunction(name)
		if err != nil || fn.ActiveVersion == "" {
			return "", false
		}
		return fn.OwnerID, true
	}
	sc.ListOwn = func(ownerID string) []string {
		fns, err := st.ListFunctionsByOwner(ownerID)
		if err != nil {
			return []string{}
		}
		names := make([]string, 0, len(fns))
		for _, f := range fns {
			names = append(names, f.Name)
		}
		return names
	}
	sc.Invoker = &sidecarInvoker{backend: backend, store: st, sidecarURL: sidecarURL}

	helloImage := getenv("HELLO_IMAGE", "hello-ts:latest")

	sched := scheduler.New(st, backend, qbus, helloImage, sidecarURL)
	sched.Start()
	defer sched.Stop()

	deployWorker := &deploy.Worker{Svc: svc, OnActivated: func(string) { sched.Sync() }}
	deployWorker.Start()
	defer deployWorker.Stop()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		i, c, warm := backend.Metrics()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"invokes": i, "cold_starts": c, "warm": warm,
		})
	})
	controlauth.RegisterAuthRoutes(mux, st)
	controlauth.RegisterKeyRoutes(mux, st)
	gwHooks := controlauth.GatewayHooks(st, sidecarURL)
	gw := gateway.NewHandlerWithAuth(backend, gwHooks)
	mux.Handle("/f/", gw)
	mux.Handle("/s/", gw)
	// Operator queue ingress: POST /pub/:topic with raw body -> bus ->
	// subscriber functions. (Handlers publish via sidecar with tokens.)
	// Requires operator login once users exist.
	mux.HandleFunc("POST /pub/", controlauth.RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
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
	}))
	sc.RegisterRoutes(mux)
	sidecar.RegisterTransferRoutesWithOwner(mux, arts, tokens, sc.OwnerOf)
	mux.HandleFunc("POST /deploy", controlauth.RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name       string `json:"name"`
			ConfigTOML string `json:"config_toml"`
			SrcZipB64  string `json:"src_zip_b64"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&in); err != nil {
			http.Error(w, "bad JSON (max 32MB)", http.StatusBadRequest)
			return
		}
		if err := auth.ValidateFunctionName(in.Name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		me, ok := controlauth.CurrentUser(st, r)
		ownerID := ""
		if ok {
			ownerID = me.ID
			// Ownership: refuse to overwrite another user's function.
			if existing, err := st.GetFunction(in.Name); err == nil && existing.OwnerID != "" && existing.OwnerID != ownerID {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		zipBytes, err := base64.StdEncoding.DecodeString(in.SrcZipB64)
		if err != nil || len(zipBytes) == 0 {
			http.Error(w, "src_zip_b64 must be non-empty base64", http.StatusBadRequest)
			return
		}
		job, err := svc.Enqueue(r.Context(), deploy.Request{Name: in.Name, ConfigTOML: in.ConfigTOML, SrcZip: zipBytes, OwnerID: ownerID})
		if err != nil {
			slog.Warn("deploy enqueue failed", "fn", in.Name, "err", err)
			http.Error(w, "deploy: "+err.Error(), http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("wait") != "1" && r.URL.Query().Get("sync") != "1" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(job)
			return
		}
		// Synchronous compat: wait for the worker to finish the job.
		deadline := time.Now().Add(110 * time.Second)
		for {
			j, err := st.GetJob(job.ID)
			if err != nil {
				http.Error(w, "job lost", http.StatusInternalServerError)
				return
			}
			switch j.Status {
			case store.JobActive:
				sched.Sync()
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(deploy.Result{
					Name: j.FnName, Version: j.Version, Status: j.Status,
					Image: j.Image, SHA256: j.SHA256,
				})
				return
			case store.JobFailed:
				http.Error(w, "deploy "+j.ID+" failed: "+j.Error, http.StatusBadGateway)
				return
			}
			if time.Now().After(deadline) {
				http.Error(w, "deploy "+j.ID+" still "+j.Status+" (poll GET /deploys/"+j.ID+")", http.StatusAccepted)
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Second):
			}
		}
	}))
	mux.HandleFunc("GET /deploys", controlauth.RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
		fn := r.URL.Query().Get("fn")
		if fn == "" {
			http.Error(w, "?fn= required", http.StatusBadRequest)
			return
		}
		if me, ok := controlauth.CurrentUser(st, r); ok {
			if existing, err := st.GetFunction(fn); err == nil && existing.OwnerID != "" && existing.OwnerID != me.ID {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		jobs, err := st.ListJobs(fn, 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if jobs == nil {
			jobs = []store.Job{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jobs)
	}))
	mux.HandleFunc("GET /deploys/", controlauth.RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/deploys/")
		id, tail, _ := strings.Cut(rest, "/")
		j, err := st.GetJob(id)
		if err != nil {
			http.Error(w, "unknown job", http.StatusNotFound)
			return
		}
		if me, ok := controlauth.CurrentUser(st, r); ok && j.OwnerID != "" && j.OwnerID != me.ID {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if tail == "logs" {
			if j.LogKey == "" {
				http.Error(w, "no logs yet", http.StatusNotFound)
				return
			}
			raw, err := arts.Get(r.Context(), j.LogKey)
			if err != nil {
				http.Error(w, "no logs yet", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write(raw)
			return
		}
		if tail != "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(j)
	}))
	mux.HandleFunc("GET /functions", controlauth.RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
		var fns []store.Function
		var err error
		if me, ok := controlauth.CurrentUser(st, r); ok {
			fns, err = st.ListFunctionsByOwner(me.ID)
		} else {
			fns, err = st.ListFunctions() // fresh bootstrap: zero users
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if fns == nil {
			fns = []store.Function{}
		}
		warm := backend.Status()
		type entry struct {
			store.Function
			DeployedAt string                  `json:"DeployedAt"`
			Warm       []runner.InstanceStatus `json:"Warm"`
		}
		out := make([]entry, 0, len(fns))
		for _, f := range fns {
			e := entry{Function: f, DeployedAt: st.ActiveVersionCreatedAt(f.Name, f.ActiveVersion)}
			for _, inst := range warm {
				if inst.Name == f.Name {
					e.Warm = append(e.Warm, inst)
				}
			}
			if e.Warm == nil {
				e.Warm = []runner.InstanceStatus{}
			}
			out = append(out, e)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	mux.HandleFunc("GET /logs", controlauth.RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
		fn := r.URL.Query().Get("fn")
		if fn == "" {
			http.Error(w, "?fn= required", http.StatusBadRequest)
			return
		}
		if me, ok := controlauth.CurrentUser(st, r); ok {
			if existing, err := st.GetFunction(fn); err == nil && existing.OwnerID != "" && existing.OwnerID != me.ID {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(logs.Tail(fn, 100))
	}))
	mux.HandleFunc("GET /versions", controlauth.RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
		fn := r.URL.Query().Get("fn")
		if fn == "" {
			http.Error(w, "?fn= required", http.StatusBadRequest)
			return
		}
		if me, ok := controlauth.CurrentUser(st, r); ok {
			if existing, err := st.GetFunction(fn); err == nil && existing.OwnerID != "" && existing.OwnerID != me.ID {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		vers, err := st.ListVersions(fn)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if vers == nil {
			vers = []store.Version{}
		}
		sort.SliceStable(vers, func(i, j int) bool {
			vi, ei := deploy.ParseVersion(vers[i].Ver)
			vj, ej := deploy.ParseVersion(vers[j].Ver)
			if ei != nil || ej != nil {
				return vers[i].Ver < vers[j].Ver
			}
			return deploy.Compare(vi, vj) < 0
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(vers)
	}))
	mux.HandleFunc("POST /rollback", controlauth.RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string `json:"name"`
			Ver  string `json:"ver"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&in); err != nil {
			http.Error(w, "bad JSON", http.StatusBadRequest)
			return
		}
		if me, ok := controlauth.CurrentUser(st, r); ok {
			if existing, err := st.GetFunction(in.Name); err == nil && existing.OwnerID != "" && existing.OwnerID != me.ID {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		if err := st.SetActiveVersion(in.Name, in.Ver); err != nil {
			http.Error(w, "rollback: "+err.Error(), http.StatusBadRequest)
			return
		}
		sched.Sync()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"name": in.Name, "active_version": in.Ver})
	}))
	mux.HandleFunc("DELETE /functions", controlauth.RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
		fn := r.URL.Query().Get("fn")
		if fn == "" {
			http.Error(w, "?fn= required", http.StatusBadRequest)
			return
		}
		if me, ok := controlauth.CurrentUser(st, r); ok {
			if existing, err := st.GetFunction(fn); err == nil && existing.OwnerID != "" && existing.OwnerID != me.ID {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		if err := st.DeleteFunction(fn); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sched.Sync()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"deleted": fn})
	}))

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
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server exited", "err", err)
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
