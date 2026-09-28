package gateway

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
	"strings"
	"time"

	"actions/internal/runner"
)

// Wire format (protojson-compatible, hand-rollable):
//   InvokeRequest  {event:{method,path,headers,query,body:<base64>}, ctx:{...}}
//   InvokeResponse {status, headers, body:<base64>}
// bytes fields travel base64 so binaries without proto libs can use plain JSON.
type httpEvent struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Query   map[string]string `json:"query"`
	Body    string            `json:"body"` // base64
}

type invokeCtx struct {
	FunctionName string `json:"function_name"`
	Version      string `json:"version"`
	RequestID    string `json:"request_id"`
	DeadlineMs   int64  `json:"deadline_ms"`
	SidecarURL   string `json:"sidecar_url,omitempty"`
}

type invokeRequest struct {
	Event httpEvent `json:"event"`
	Ctx   invokeCtx `json:"ctx"`
}

type Invoker interface {
	Invoke(ctx context.Context, ref runner.FunctionRef, invokeRequestJSON []byte) (*runner.InvokeResponse, error)
}

// Lookup resolves /f/:name to a runnable function. SQLite-backed in Phase 2;
// in-memory single entry (hello) for the Phase 1 slice.
type Lookup func(name string) (runner.FunctionRef, bool)

func NewHandler(invoker interface {
	Invoke(ctx context.Context, ref runner.FunctionRef, invokeRequestJSON []byte) (*runner.InvokeResponse, error)
}, lookup Lookup) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		name, rest := splitFnPath(r.URL.Path)
		if name == "" {
			http.Error(w, "missing function name", http.StatusNotFound)
			return
		}
		ref, ok := lookup(name)
		if !ok {
			http.Error(w, "unknown function "+name, http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		headers := map[string]string{}
		for k := range r.Header {
			headers[strings.ToLower(k)] = r.Header.Get(k)
		}
		query := map[string]string{}
		for k := range r.URL.Query() {
			query[k] = r.URL.Query().Get(k)
		}
		timeout := runner.ClampTimeout(ref.Timeout)
		ireq := invokeRequest{
			Event: httpEvent{
				Method:  r.Method,
				Path:    "/" + rest,
				Headers: headers,
				Query:   query,
				Body:    base64.StdEncoding.EncodeToString(body),
			},
			Ctx: invokeCtx{
				FunctionName: ref.Name,
				Version:      ref.Version,
				RequestID:    requestID(),
				DeadlineMs:   time.Now().Add(timeout).UnixMilli(),
				SidecarURL:   sidecarURL(ref),
			},
		}
		payload, err := json.Marshal(ireq)
		if err != nil {
			http.Error(w, "encode invoke", http.StatusInternalServerError)
			return
		}
		resp, err := invoker.Invoke(r.Context(), ref, payload)
		if err != nil {
			slog.Warn("invoke failed", "fn", name, "method", r.Method, "path", r.URL.Path, "dur_ms", time.Since(start).Milliseconds(), "err", err)
			http.Error(w, "invoke: "+err.Error(), http.StatusBadGateway)
			return
		}
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		status := resp.Status
		if status == 0 {
			status = http.StatusOK
		}
		raw, err := base64.StdEncoding.DecodeString(resp.Body)
		if err != nil {
			http.Error(w, "handler returned non-base64 body", http.StatusBadGateway)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		slog.Info("invoke", "fn", name, "method", r.Method, "path", r.URL.Path, "status", status, "dur_ms", time.Since(start).Milliseconds())
	})
}

// splitFnPath turns /f/hello/a/b into ("hello", "a/b").
func splitFnPath(p string) (string, string) {
	trimmed := strings.TrimPrefix(p, "/f/")
	if trimmed == p {
		return "", ""
	}
	name, rest, _ := strings.Cut(trimmed, "/")
	return name, rest
}

func requestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func sidecarURL(ref runner.FunctionRef) string {
	if ref.SidecarURL != "" {
		return ref.SidecarURL
	}
	if v := os.Getenv("ACTIONS_SIDECAR_URL"); v != "" {
		return v
	}
	return ""
}
