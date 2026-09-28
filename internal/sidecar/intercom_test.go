package sidecar

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"actions/internal/bus"
)

func intercomServer() (*Server, *Registry) {
	reg := NewRegistry()
	s := &Server{KV: NewMemKV(), Blobs: &LocalBlob{ControlURL: "http://x"}, Bus: bus.NewMemory(), Logs: NewLogRing(), Tokens: reg}
	owners := map[string]string{"ui": "u1", "echo": "u1", "other": "u2"}
	s.OwnerOf = func(name string) (string, bool) {
		o, ok := owners[name]
		return o, ok
	}
	s.ListOwn = func(ownerID string) []string {
		var out []string
		for fn, o := range owners {
			if o == ownerID {
				out = append(out, fn)
			}
		}
		if out == nil {
			out = []string{}
		}
		return out
	}
	return s, reg
}

func postWithToken(t *testing.T, mux http.Handler, path string, v any, tok string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(v)
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	if tok != "" {
		req.Header.Set("X-Actions-Token", tok)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

func TestIntercomLogsAllowed(t *testing.T) {
	s, reg := intercomServer()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	s.Logs.Append("echo", "v1", "r1", "hello-log")

	uiTok := reg.MintWithPolicy("ui", "u1", []string{"echo"}, nil, nil, nil)
	code, out := postWithToken(t, mux, "/sidecar/logs/tail", map[string]any{"target_function": "echo"}, uiTok)
	if code != 200 {
		t.Fatalf("tail -> %d (%v), want 200", code, out)
	}
	lines, _ := out["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want 1", out)
	}
}

func TestIntercomLogsDeniedNoAllow(t *testing.T) {
	s, reg := intercomServer()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	uiTok := reg.MintWithPolicy("ui", "u1", nil, nil, nil, nil)
	if code, _ := postWithToken(t, mux, "/sidecar/logs/tail", map[string]any{"target_function": "echo"}, uiTok); code != 403 {
		t.Fatalf("no allow -> %d, want 403", code)
	}
}

func TestIntercomLogsDeniedCrossOwner(t *testing.T) {
	s, reg := intercomServer()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	// "*" still must not cross owners.
	uiTok := reg.MintWithPolicy("ui", "u1", []string{"*"}, nil, nil, nil)
	if code, _ := postWithToken(t, mux, "/sidecar/logs/tail", map[string]any{"target_function": "other"}, uiTok); code != 403 {
		t.Fatalf("cross-owner -> %d, want 403", code)
	}
}

func TestIntercomLogsWildcardSameOwner(t *testing.T) {
	s, reg := intercomServer()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	s.Logs.Append("echo", "v1", "r1", "x")
	uiTok := reg.MintWithPolicy("ui", "u1", []string{"*"}, nil, nil, nil)
	if code, _ := postWithToken(t, mux, "/sidecar/logs/tail", map[string]any{"target_function": "echo"}, uiTok); code != 200 {
		t.Fatalf("wildcard same-owner -> %d, want 200", code)
	}
}

func TestIntercomKVAllowed(t *testing.T) {
	s, reg := intercomServer()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	echoTok := reg.Mint("echo")
	// seed echo's key as echo itself
	reqBody, _ := json.Marshal(map[string]any{"function_name": "echo", "key": "k", "value": base64.StdEncoding.EncodeToString([]byte("v"))})
	req := httptest.NewRequest("POST", "/sidecar/kv/put", bytes.NewReader(reqBody))
	req.Header.Set("X-Actions-Token", echoTok)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("seed -> %d", rec.Code)
	}
	uiTok := reg.MintWithPolicy("ui", "u1", nil, nil, []string{"echo"}, nil)
	code, out := postWithToken(t, mux, "/sidecar/kv/get", map[string]any{"function_name": "echo", "key": "k"}, uiTok)
	if code != 200 || out["found"] != true {
		t.Fatalf("kv intercom -> %d %v, want 200 found", code, out)
	}
	// without allow -> 403
	plain := reg.MintWithPolicy("ui", "u1", nil, nil, nil, nil)
	if code, _ := postWithToken(t, mux, "/sidecar/kv/get", map[string]any{"function_name": "echo", "key": "k"}, plain); code != 403 {
		t.Fatalf("kv no allow -> %d, want 403", code)
	}
}

type fakeInvoker struct {
	called string
}

func (f *fakeInvoker) Invoke(_ context.Context, target, method, path string, headers, query map[string]string, body []byte) (int, map[string]string, []byte, error) {
	f.called = target
	return 200, map[string]string{"content-type": "text/plain"}, []byte("ok-from-" + target), nil
}

func TestIntercomInvoke(t *testing.T) {
	s, reg := intercomServer()
	mux := http.NewServeMux()
	fk := &fakeInvoker{}
	s.Invoker = fk
	s.RegisterRoutes(mux)
	uiTok := reg.MintWithPolicy("ui", "u1", nil, []string{"echo"}, nil, nil)
	code, out := postWithToken(t, mux, "/sidecar/invoke", map[string]any{"target_function": "echo", "method": "GET", "path": "/"}, uiTok)
	if code != 200 {
		t.Fatalf("invoke -> %d %v", code, out)
	}
	if fk.called != "echo" {
		t.Fatalf("invoker called %q", fk.called)
	}
	// denied without allow
	plain := reg.MintWithPolicy("ui", "u1", nil, nil, nil, nil)
	if code, _ := postWithToken(t, mux, "/sidecar/invoke", map[string]any{"target_function": "echo"}, plain); code != 403 {
		t.Fatalf("invoke no allow -> %d, want 403", code)
	}
}

func TestIntercomFunctionsListScoped(t *testing.T) {
	s, reg := intercomServer()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	uiTok := reg.MintWithPolicy("ui", "u1", nil, nil, nil, nil)
	code, out := postWithToken(t, mux, "/sidecar/functions/list", map[string]any{}, uiTok)
	if code != 200 {
		t.Fatalf("list -> %d", code)
	}
	fns, _ := out["functions"].([]any)
	names := map[string]bool{}
	for _, f := range fns {
		names[f.(string)] = true
	}
	if !names["ui"] || !names["echo"] || names["other"] {
		t.Fatalf("scoped list wrong: %v", names)
	}
}
