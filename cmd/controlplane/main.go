package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}
