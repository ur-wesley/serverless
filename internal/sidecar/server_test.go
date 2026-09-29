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
	"time"

	"github.com/ur-wesley/oort/internal/bus"
)

func testServer() *Server {
	return &Server{KV: NewMemKV(), Blobs: &LocalBlob{ControlURL: "http://x"}, Bus: bus.NewMemory(), Logs: NewLogRing()}
}

func post(t *testing.T, mux http.Handler, path string, v any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(v)
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != 200 {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("%s -> %d: %s", path, res.StatusCode, raw)
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestKVRoundTrip(t *testing.T) {
	s := testServer()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	val := base64.StdEncoding.EncodeToString([]byte("v1"))
	post(t, mux, "/sidecar/kv/put", map[string]any{"function_name": "f", "key": "k", "value": val})
	got := post(t, mux, "/sidecar/kv/get", map[string]any{"function_name": "f", "key": "k"})
	if got["found"] != true {
		t.Fatalf("found = %v", got)
	}
	raw, _ := base64.StdEncoding.DecodeString(got["value"].(string))
	if string(raw) != "v1" {
		t.Fatalf("value = %q", raw)
	}
	post(t, mux, "/sidecar/kv/del", map[string]any{"function_name": "f", "key": "k"})
	got = post(t, mux, "/sidecar/kv/get", map[string]any{"function_name": "f", "key": "k"})
	if got["found"] != false {
		t.Fatalf("found after del = %v", got)
	}
}

func TestKVTTLExpiry(t *testing.T) {
	kv := NewMemKV()
	kv.Put("f", "k", []byte("v"), 50*time.Millisecond)
	if _, ok := kv.Get("f", "k"); !ok {
		t.Fatal("should exist before expiry")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := kv.Get("f", "k"); ok {
		t.Fatal("should have expired")
	}
}

func TestUnwrapNatsKVValue(t *testing.T) {
	plain := []byte("hello")
	if v, ok := unwrapNatsKVValue(plain); !ok || string(v) != "hello" {
		t.Fatalf("plain passthrough failed: %q %v", v, ok)
	}
	wrapped, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Minute).Unix(), "v": []byte("ttl-val")})
	if v, ok := unwrapNatsKVValue(wrapped); !ok || string(v) != "ttl-val" {
		t.Fatalf("ttl unwrap failed: %q %v", v, ok)
	}
	expired, _ := json.Marshal(map[string]any{"exp": time.Now().Add(-time.Minute).Unix(), "v": []byte("old")})
	if _, ok := unwrapNatsKVValue(expired); ok {
		t.Fatal("expired should be not-found")
	}
}

func TestQueuePublishReachesBus(t *testing.T) {
	s := testServer()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	ch := make(chan string, 1)
	unsub, err := s.Bus.Subscribe("orders.created", func(_ context.Context, msg []byte) error { ch <- string(msg); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	post(t, mux, "/sidecar/queue/publish", map[string]any{
		"topic": "orders.created",
		"message": base64.StdEncoding.EncodeToString([]byte("order-1")),
	})
	select {
	case m := <-ch:
		if m != "order-1" {
			t.Fatalf("msg = %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message received")
	}
}

func TestBlobLocalURLs(t *testing.T) {
	s := testServer()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	out := post(t, mux, "/sidecar/blob/put-url", map[string]any{"function_name": "f", "key": "a.bin"})
	u, _ := out["url"].(string)
	if u != "http://x/blob/ul?key=blobs%2Ff%2Fa.bin" {
		t.Fatalf("put url = %q", u)
	}
}

func TestLogAppend(t *testing.T) {
	s := testServer()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	post(t, mux, "/sidecar/log/append", map[string]any{"function_name": "f", "version": "v1", "request_id": "r", "line": "hi"})
	if tail := s.Logs.Tail("f", 10); len(tail) != 1 || tail[0].Line != "hi" {
		t.Fatalf("tail = %+v", tail)
	}
}

func TestAuthEnforced(t *testing.T) {
	s := testServer()
	reg := NewRegistry()
	s.Tokens = reg
	tok := reg.Mint("f")
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	body := map[string]any{"function_name": "f", "key": "k", "value": base64.StdEncoding.EncodeToString([]byte("v"))}
	req := httptest.NewRequest("POST", "/sidecar/kv/put", bytes.NewReader(mustJSON(t, body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no token -> %d, want 403", rec.Code)
	}
	req = httptest.NewRequest("POST", "/sidecar/kv/put", bytes.NewReader(mustJSON(t, body)))
	req.Header.Set("X-Actions-Token", reg.Mint("other"))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong fn token -> %d, want 403", rec.Code)
	}
	req = httptest.NewRequest("POST", "/sidecar/kv/put", bytes.NewReader(mustJSON(t, body)))
	req.Header.Set("X-Actions-Token", tok)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token -> %d, want 200", rec.Code)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
