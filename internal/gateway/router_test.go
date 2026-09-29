package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ur-wesley/oort/internal/runner"
)

type fakeInvoker struct {
	gotPayload []byte
	resp       *runner.InvokeResponse
	err        error
}

func (f *fakeInvoker) Invoke(_ context.Context, _ runner.FunctionRef, p []byte) (*runner.InvokeResponse, error) {
	f.gotPayload = p
	return f.resp, f.err
}

func TestHandlerProxiesInvokeAndDecodesBody(t *testing.T) {
	inv := &fakeInvoker{resp: &runner.InvokeResponse{
		Status:  201,
		Headers: map[string]string{"content-type": "text/plain"},
		Body:    base64.StdEncoding.EncodeToString([]byte("hi")),
	}}
	h := NewHandler(inv, func(name string) (runner.FunctionRef, bool) {
		if name == "hello" {
			return runner.FunctionRef{Name: "hello", Version: "dev", Image: "hello-ts:latest"}, true
		}
		return runner.FunctionRef{}, false
	})

	req := httptest.NewRequest("POST", "/f/hello/a/b?x=1", strings.NewReader("ping"))
	req.Header.Set("X-Test", "yes")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	raw, _ := io.ReadAll(res.Body)
	if string(raw) != "hi" {
		t.Fatalf("body = %q, want %q", raw, "hi")
	}
	if got := res.Header.Get("content-type"); got != "text/plain" {
		t.Fatalf("content-type = %q", got)
	}
	var payload struct {
		Event struct {
			Method string `json:"method"`
			Path   string `json:"path"`
			Body   string `json:"body"`
		} `json:"event"`
	}
	if err := json.Unmarshal(inv.gotPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Event.Path != "/a/b" || payload.Event.Method != "POST" {
		t.Fatalf("event routed wrong: %+v", payload.Event)
	}
	decoded, _ := base64.StdEncoding.DecodeString(payload.Event.Body)
	if string(decoded) != "ping" {
		t.Fatalf("event body = %q, want ping", decoded)
	}
}

func TestHandlerUnknownFunction404(t *testing.T) {
	inv := &fakeInvoker{}
	h := NewHandler(inv, func(string) (runner.FunctionRef, bool) { return runner.FunctionRef{}, false })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/f/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestSplitFnPath(t *testing.T) {
	name, rest := splitFnPath("/f/hello/a/b")
	if name != "hello" || rest != "a/b" {
		t.Fatalf("got %q %q", name, rest)
	}
}
