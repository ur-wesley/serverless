// Package sdk is the thin hand-written Go wrapper over gen/go.
// Generated protobuf types carry the contract; this file only adds
// ergonomic Serve/HandleFunc and the sidecar Env client.
package sdk

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

type Event struct {
	Method  string
	Path    string
	Headers map[string]string
	Query   map[string]string
	Body    []byte
}

type Response struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

type Ctx struct {
	FunctionName string
	Version      string
	RequestID    string
	DeadlineMs   int64
	SidecarURL   string
}

type HandlerFunc func(ctx context.Context, c Ctx, e Event) Response

// HandleFunc serves GET /healthz + POST /invoke on $PORT (or 3000).
func HandleFunc(h HandlerFunc) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	slog.Info("handler listening", "port", port)
	if err := http.ListenAndServe(":"+port, NewHandler(h)); err != nil {
		slog.Error("exited", "err", err)
		os.Exit(1)
	}
}

// NewHandler builds the ABI mux for embedding and tests.
func NewHandler(h HandlerFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /invoke", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Event struct {
				Method  string            `json:"method"`
				Path    string            `json:"path"`
				Headers map[string]string `json:"headers"`
				Query   map[string]string `json:"query"`
				Body    string            `json:"body"`
			} `json:"event"`
			Ctx struct {
				FunctionName string `json:"function_name"`
				Version      string `json:"version"`
				RequestID    string `json:"request_id"`
				DeadlineMs   int64  `json:"deadline_ms"`
				SidecarURL   string `json:"sidecar_url"`
			} `json:"ctx"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&in); err != nil {
			writeResp(w, Response{Status: 400, Body: []byte("bad invoke JSON")})
			return
		}
		raw, _ := base64.StdEncoding.DecodeString(in.Event.Body)
		out := h(r.Context(), Ctx{
			FunctionName: in.Ctx.FunctionName, Version: in.Ctx.Version,
			RequestID: in.Ctx.RequestID, DeadlineMs: in.Ctx.DeadlineMs,
			SidecarURL: in.Ctx.SidecarURL,
		}, Event{
			Method: in.Event.Method, Path: in.Event.Path,
			Headers: in.Event.Headers, Query: in.Event.Query, Body: raw,
		})
		writeResp(w, out)
	})
	return mux
}

func writeResp(w http.ResponseWriter, r Response) {
	for k, v := range r.Headers {
		w.Header().Set(k, v)
	}
	status := r.Status
	if status == 0 {
		status = 200
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  status,
		"headers": r.Headers,
		"body":    base64.StdEncoding.EncodeToString(r.Body),
	})
}

// --- Sidecar client ---

type Env struct {
	BaseURL string
	Client  *http.Client
}

func FromEnv() Env {
	u := os.Getenv("ACTIONS_SIDECAR_URL")
	return Env{BaseURL: u, Client: &http.Client{Timeout: 10 * time.Second}}
}

func (e Env) post(ctx context.Context, path string, in, out any) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := os.Getenv("ACTIONS_SIDECAR_TOKEN"); tok != "" {
		req.Header.Set("X-Actions-Token", tok)
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16 * 1024))
		return fmt.Errorf("%s: %d: %s", path, resp.StatusCode, string(raw))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (e Env) KVGet(ctx context.Context, fn, key string) ([]byte, bool, error) {
	var out struct {
		Value string `json:"value"`
		Found bool   `json:"found"`
	}
	if err := e.post(ctx, "/sidecar/kv/get", map[string]string{"function_name": fn, "key": key}, &out); err != nil {
		return nil, false, err
	}
	raw, err := base64.StdEncoding.DecodeString(out.Value)
	return raw, out.Found, err
}

func (e Env) KVPut(ctx context.Context, fn, key string, value []byte, ttl time.Duration) error {
	return e.post(ctx, "/sidecar/kv/put", map[string]any{
		"function_name": fn, "key": key,
		"value":       base64.StdEncoding.EncodeToString(value),
		"ttl_seconds": int64(ttl / time.Second),
	}, nil)
}

func (e Env) KVDel(ctx context.Context, fn, key string) error {
	return e.post(ctx, "/sidecar/kv/del", map[string]string{"function_name": fn, "key": key}, nil)
}

func (e Env) blobURL(ctx context.Context, op, fn, key string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	err := e.post(ctx, "/sidecar/blob/"+op, map[string]string{"function_name": fn, "key": key}, &out)
	return out.URL, err
}

// BlobPutBytes uploads via presigned PUT (S3) or the local transfer endpoint.
func (e Env) BlobPutBytes(ctx context.Context, fn, key string, data []byte) error {
	u, err := e.blobURL(ctx, "put-url", fn, key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	if tok := os.Getenv("ACTIONS_SIDECAR_TOKEN"); tok != "" {
		req.Header.Set("X-Actions-Token", tok)
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("blob put: %d", resp.StatusCode)
	}
	return nil
}

func (e Env) BlobGetBytes(ctx context.Context, fn, key string) ([]byte, error) {
	u, err := e.blobURL(ctx, "get-url", fn, key)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	if tok := os.Getenv("ACTIONS_SIDECAR_TOKEN"); tok != "" {
		req.Header.Set("X-Actions-Token", tok)
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("blob get: %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

func (e Env) QueuePublish(ctx context.Context, topic string, msg []byte) error {
	return e.post(ctx, "/sidecar/queue/publish", map[string]string{
		"topic": topic, "message": base64.StdEncoding.EncodeToString(msg),
	}, nil)
}

func (e Env) Logf(ctx context.Context, fn, ver, reqID, format string, args ...any) error {
	return e.post(ctx, "/sidecar/log/append", map[string]string{
		"function_name": fn, "version": ver, "request_id": reqID,
		"line": fmt.Sprintf(format, args...),
	}, nil)
}

type LogLine struct {
	Function  string `json:"Function"`
	Version   string `json:"Version"`
	RequestID string `json:"RequestID"`
	Line      string `json:"Line"`
	Time      string `json:"Time"`
}

// LogsTail reads another function's logs via intercom (allow_logs + same owner).
func (e Env) LogsTail(ctx context.Context, target string, limit int) ([]LogLine, error) {
	var out struct {
		Lines []LogLine `json:"lines"`
	}
	if err := e.post(ctx, "/sidecar/logs/tail", map[string]any{
		"target_function": target, "limit": limit,
	}, &out); err != nil {
		return nil, err
	}
	return out.Lines, nil
}

// FunctionsList returns own function names for dashboard discovery.
func (e Env) FunctionsList(ctx context.Context) ([]string, error) {
	var out struct {
		Functions []string `json:"functions"`
	}
	if err := e.post(ctx, "/sidecar/functions/list", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Functions, nil
}

type InvokeResult struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    []byte            `json:"-"`
}

// InvokeOther calls another function via intercom (allow_invoke + same owner).
func (e Env) InvokeOther(ctx context.Context, target, method, path string, headers, query map[string]string, body []byte) (InvokeResult, error) {
	var out struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if err := e.post(ctx, "/sidecar/invoke", map[string]any{
		"target_function": target, "method": method, "path": path,
		"headers": headers, "query": query,
		"body": base64.StdEncoding.EncodeToString(body),
	}, &out); err != nil {
		return InvokeResult{}, err
	}
	raw, err := base64.StdEncoding.DecodeString(out.Body)
	if err != nil {
		return InvokeResult{}, err
	}
	return InvokeResult{Status: out.Status, Headers: out.Headers, Body: raw}, nil
}
