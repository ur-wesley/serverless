package gateway

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"actions/internal/runner"
)

func testHooks() AuthHooks {
	pub := FuncInfo{Ref: runner.FunctionRef{Name: "pub", Version: "v1"}, AuthMode: "public"}
	keyed := FuncInfo{Ref: runner.FunctionRef{Name: "locked", Version: "v1"}, AuthMode: "key", OwnerID: "u1"}
	priv := FuncInfo{Ref: runner.FunctionRef{Name: "secret", Version: "v1"}, AuthMode: "private", OwnerID: "u1"}
	ns := FuncInfo{Ref: runner.FunctionRef{Name: "hello", Version: "v1"}, AuthMode: "public", OwnerID: "u9"}
	bySlug := FuncInfo{Ref: runner.FunctionRef{Name: "locked", Version: "v1"}, AuthMode: "key", OwnerID: "u1"}
	return AuthHooks{
		Lookup: func(r *http.Request, name string) (FuncInfo, bool) {
			switch name {
			case "pub":
				return pub, true
			case "locked":
				return keyed, true
			case "secret":
				return priv, true
			}
			return FuncInfo{}, false
		},
		LookupNS: func(r *http.Request, owner, name string) (FuncInfo, bool) {
			if owner == "alice" && name == "hello" {
				return ns, true
			}
			return FuncInfo{}, false
		},
		LookupSlug: func(r *http.Request, slug string) (FuncInfo, bool) {
			if slug == "abcd1234" {
				return bySlug, true
			}
			return FuncInfo{}, false
		},
		CheckKey: func(fn, key string) bool { return fn == "locked" && key == "good" },
		OperatorOf: func(r *http.Request) (string, bool) {
			if r.Header.Get("Authorization") == "Bearer op-u1" {
				return "u1", true
			}
			return "", false
		},
	}
}

func invoke(t *testing.T, h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPublicOpen(t *testing.T) {
	inv := &fakeInvoker{resp: &runner.InvokeResponse{Body: base64.StdEncoding.EncodeToString([]byte("ok"))}}
	h := NewHandlerWithAuth(inv, testHooks())
	if rec := invoke(t, h, "GET", "/f/pub/hi", nil); rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestKeyEnforced(t *testing.T) {
	inv := &fakeInvoker{resp: &runner.InvokeResponse{Body: base64.StdEncoding.EncodeToString([]byte("ok"))}}
	h := NewHandlerWithAuth(inv, testHooks())
	if rec := invoke(t, h, "GET", "/f/locked/hi", nil); rec.Code != 401 {
		t.Fatalf("no-key status = %d, want 401", rec.Code)
	}
	if rec := invoke(t, h, "GET", "/f/locked/hi?key=good", nil); rec.Code != 200 {
		t.Fatalf("query-key status = %d, want 200", rec.Code)
	}
	if rec := invoke(t, h, "GET", "/f/locked/hi", map[string]string{"x-api-key": "good"}); rec.Code != 200 {
		t.Fatalf("header-key status = %d, want 200", rec.Code)
	}
	// Owner operator bypasses key.
	if rec := invoke(t, h, "GET", "/f/locked/hi", map[string]string{"Authorization": "Bearer op-u1"}); rec.Code != 200 {
		t.Fatalf("owner status = %d, want 200", rec.Code)
	}
}

func TestPrivateOwnerOnly(t *testing.T) {
	inv := &fakeInvoker{resp: &runner.InvokeResponse{Body: base64.StdEncoding.EncodeToString([]byte("ok"))}}
	h := NewHandlerWithAuth(inv, testHooks())
	if rec := invoke(t, h, "GET", "/f/secret/hi", nil); rec.Code != 401 {
		t.Fatalf("anon status = %d, want 401", rec.Code)
	}
	if rec := invoke(t, h, "GET", "/f/secret/hi", map[string]string{"x-api-key": "good"}); rec.Code != 401 {
		t.Fatalf("key status = %d, want 401", rec.Code)
	}
	if rec := invoke(t, h, "GET", "/f/secret/hi", map[string]string{"Authorization": "Bearer op-u1"}); rec.Code != 200 {
		t.Fatalf("owner status = %d, want 200", rec.Code)
	}
}

func TestNamespacedAndSlug(t *testing.T) {
	inv := &fakeInvoker{resp: &runner.InvokeResponse{Body: base64.StdEncoding.EncodeToString([]byte("ok"))}}
	h := NewHandlerWithAuth(inv, testHooks())
	if rec := invoke(t, h, "GET", "/f/alice/hello/world", nil); rec.Code != 200 {
		t.Fatalf("ns status = %d, want 200", rec.Code)
	}
	var _ = context.Background
	if rec := invoke(t, h, "GET", "/s/abcd1234/hi?key=good", nil); rec.Code != 200 {
		t.Fatalf("slug status = %d, want 200", rec.Code)
	}
	if rec := invoke(t, h, "GET", "/s/abcd1234/hi", nil); rec.Code != 401 {
		t.Fatalf("slug no-key status = %d, want 401", rec.Code)
	}
	if rec := invoke(t, h, "GET", "/f/nope/x", nil); rec.Code != 404 {
		t.Fatalf("unknown status = %d, want 404", rec.Code)
	}
}
