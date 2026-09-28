package controlauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"actions/internal/auth"
	"actions/internal/store"
)

func openTest(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestDeviceFlowEndToEnd(t *testing.T) {
	st := openTest(t)
	mux := http.NewServeMux()
	RegisterAuthRoutes(mux, st)

	// Start.
	req := httptest.NewRequest("POST", "/auth/device/start", strings.NewReader(`{"device_name":"cli@test"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("start = %d: %s", rec.Code, rec.Body.String())
	}
	var start struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		VerifyPath string `json:"verify_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(start.VerifyPath, "/auth/device?code=") {
		t.Fatalf("verify_path = %q", start.VerifyPath)
	}

	// Poll while pending.
	poll := func() map[string]any {
		r := httptest.NewRequest("POST", "/auth/device/poll", strings.NewReader(`{"device_code":`+quote(start.DeviceCode)+`}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if poll()["status"] != "pending" {
		t.Fatal("want pending")
	}

	// Approve via JSON (first verification creates the account).
	approveBody := `{"user_code":` + quote(start.UserCode) + `,"username":"alice","password":"password123"}`
	r := httptest.NewRequest("POST", "/auth/device/approve", strings.NewReader(approveBody))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("approve = %d: %s", w.Code, w.Body.String())
	}

	got := poll()
	if got["status"] != "approved" {
		t.Fatalf("poll = %v", got)
	}
	token, _ := got["token"].(string)
	if !strings.HasPrefix(token, "act_") {
		t.Fatalf("token = %q", token)
	}

	// /auth/me works with the token.
	mr := httptest.NewRequest("GET", "/auth/me", nil)
	mr.Header.Set("Authorization", "Bearer "+token)
	mw := httptest.NewRecorder()
	mux.ServeHTTP(mw, mr)
	var me struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(mw.Body.Bytes(), &me)
	if me.Name != "alice" {
		t.Fatalf("me = %s", mw.Body.String())
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestRequireOperatorBootstrapOpen(t *testing.T) {
	st := openTest(t)
	ok := RequireOperator(st, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	rec := httptest.NewRecorder()
	ok.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 200 {
		t.Fatal("bootstrap should be open")
	}
	// After first user, closed without token.
	h, _ := auth.HashPassword("password123")
	u, _ := st.CreateUser("alice", h)
	_ = u
	rec2 := httptest.NewRecorder()
	ok.ServeHTTP(rec2, httptest.NewRequest("GET", "/x", nil))
	if rec2.Code != 401 {
		t.Fatalf("status = %d, want 401", rec2.Code)
	}
}

func TestKeysCRUD(t *testing.T) {
	st := openTest(t)
	mux := http.NewServeMux()
	RegisterAuthRoutes(mux, st)
	RegisterKeyRoutes(mux, st)

	h, _ := auth.HashPassword("password123")
	u, _ := st.CreateUser("alice", h)
	full, _, _ := auth.NewOperatorToken()
	_ = full
	tokFull, tokHash, _ := auth.NewOperatorToken()
	_ = st.CreateSession(tokHash, u.ID, time.Now().Add(24*time.Hour))
	bearer := "Bearer " + tokFull

	_ = st.UpsertFunctionOwned(u.ID, "hello", "v1", "name=\"hello\"", "slug0001", "key")

	do := func(method, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", bearer)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	created := do("POST", "/keys", `{"fn":"hello","name":"ci"}`)
	if created.Code != 200 {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	var c struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &c)
	if !strings.HasPrefix(c.Key, "ak_") {
		t.Fatalf("key = %q", c.Key)
	}
	if !CheckKey(st, "hello", c.Key) {
		t.Fatal("CheckKey failed")
	}
	listed := do("GET", "/keys?fn=hello", "")
	var keys []map[string]any
	_ = json.Unmarshal(listed.Body.Bytes(), &keys)
	if len(keys) != 1 {
		t.Fatalf("keys = %s", listed.Body.String())
	}
	if _, has := keys[0]["key_hash"]; has {
		t.Fatal("hash must not leak in list")
	}
	revoked := do("POST", "/keys/"+c.ID+"/revoke", "")
	if revoked.Code != 200 {
		t.Fatalf("revoke = %d: %s", revoked.Code, revoked.Body.String())
	}
	if CheckKey(st, "hello", c.Key) {
		t.Fatal("revoked key still valid")
	}
}
