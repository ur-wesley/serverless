package cli

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDevEventPath(t *testing.T) {
	cases := map[string]string{
		"/f/hello/hi":    "/hi",
		"/f/hello/":      "/",
		"/f/hello":       "/",
		"/f/other/a/b":   "/a/b",
		"/hi":            "/hi",
		"/":              "/",
		"/a/b":           "/a/b",
		"":               "/",
		"/f/hello/a%20b": "/a%20b",
	}
	for in, want := range cases {
		path := in
		if i := strings.Index(path, "?"); i >= 0 {
			path = path[:i]
		}
		if got := devEventPath(path, "hello"); got != want {
			t.Errorf("devEventPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildParseInvokeRoundtrip(t *testing.T) {
	payload, err := buildInvokeRequest("GET", "/hi",
		map[string]string{"content-type": "text/plain"},
		map[string]string{"x": "1"}, []byte("ping"), "hello", "http://sidecar", 10000)
	if err != nil {
		t.Fatal(err)
	}
	// Fake handler: decode request, encode response.
	var req devInvokeRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatal(err)
	}
	if req.Event.Path != "/hi" || req.Ctx.FunctionName != "hello" || req.Ctx.Version != "dev" {
		t.Fatalf("bad request: %+v", req)
	}
	respPayload := mustMarshal(t, devInvokeResponse{
		Status:  200,
		Headers: map[string]string{"content-type": "text/plain"},
		Body:    base64.StdEncoding.EncodeToString([]byte("ok-pong")),
	})
	status, headers, body, err := parseInvokeResponse(respPayload)
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 || string(body) != "ok-pong" || headers["content-type"] != "text/plain" {
		t.Fatalf("bad response: %d %v %q", status, headers, body)
	}
}

func TestGatewayTranslatesPlainHTTP(t *testing.T) {
	// Fake ABI handler.
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mustMarshal(t, devInvokeResponse{
			Status:  200,
			Headers: map[string]string{"content-type": "text/plain"},
			Body:    base64.StdEncoding.EncodeToString([]byte("hello GET /hi")),
		}))
	}))
	defer handler.Close()

	gw := &devGateway{fnName: "hello", sidecarURL: "http://sidecar", timeoutMs: 5000}
	addr := strings.TrimPrefix(handler.URL, "http://")
	gw.handlerAddr.Store(addr)

	req := httptest.NewRequest("GET", "/f/hello/hi", nil)
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "hello GET /hi" {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}

	// Root serves index.
	req2 := httptest.NewRequest("GET", "/", nil)
	rec2 := httptest.NewRecorder()
	gw.ServeHTTP(rec2, req2)
	if rec2.Code != 200 || !strings.Contains(rec2.Body.String(), "oort dev") {
		t.Fatalf("index code=%d body=%q", rec2.Code, rec2.Body.String())
	}
}

func TestSnapshotsEqualAndWatchIgnores(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(dir, "src.ts"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "node_modules", "big.js"), []byte("a"), 0o644)
	a, err := snapshotDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a["node_modules/big.js"]; ok {
		t.Fatal("should ignore node_modules")
	}
	// Touch with different content + newer mtime.
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, "src.ts"), []byte("ab"), 0o644)
	b, _ := snapshotDir(dir)
	if snapshotsEqual(a, b) {
		t.Fatal("expected change detected")
	}
	if !snapshotsEqual(b, b) {
		t.Fatal("reflexive equal failed")
	}
}

func TestHandlerSupAddrAtomic(t *testing.T) {
	var v atomic.Value
	v.Store("127.0.0.1:1")
	if v.Load().(string) != "127.0.0.1:1" {
		t.Fatal("atomic broken")
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
