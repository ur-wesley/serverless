// Package controlauth implements operator auth (device-code login),
// per-function API keys and the require-operator middleware for the
// control plane. First browser verification creates the first account;
// later verifications bind the CLI device to the user's own account
// (self-verified, no second-person approval).
package controlauth

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"actions/internal/auth"
	"actions/internal/functions"
	"actions/internal/gateway"
	"actions/internal/runner"
	"actions/internal/store"
)

type ctxKey string

const userCtxKey ctxKey = "operator"

// OperatorOf returns the authed user ID for a request, if any.
func OperatorOf(st *store.Store) func(r *http.Request) (string, bool) {
	return func(r *http.Request) (string, bool) {
		if u, ok := UserOf(r); ok {
			return u.ID, true
		}
		tok := auth.Bearer(r.Header.Get("Authorization"))
		if tok == "" {
			return "", false
		}
		u, ok := st.GetSessionUser(auth.HashSecret(tok))
		return u.ID, ok
	}
}

// UserOf returns the operator stashed by RequireOperator.
func UserOf(r *http.Request) (store.User, bool) {
	u, ok := r.Context().Value(userCtxKey).(store.User)
	return u, ok
}

// RequireOperator enforces Bearer operator auth. Open when zero users
// exist (fresh bootstrap); otherwise 401 without a valid session token.
func RequireOperator(st *store.Store, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st.CountUsers() == 0 {
			next.ServeHTTP(w, r)
			return
		}
		tok := auth.Bearer(r.Header.Get("Authorization"))
		if tok == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		u, ok := st.GetSessionUser(auth.HashSecret(tok))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey, u)))
	}
}

// CurrentUser resolves the caller: context user or Bearer session.
// Returns ok=false on fresh-bootstrap (zero users) with empty user.
func CurrentUser(st *store.Store, r *http.Request) (store.User, bool) {
	if u, ok := UserOf(r); ok {
		return u, true
	}
	if st.CountUsers() == 0 {
		return store.User{}, false
	}
	tok := auth.Bearer(r.Header.Get("Authorization"))
	if tok == "" {
		return store.User{}, false
	}
	return st.GetSessionUser(auth.HashSecret(tok))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// GatewayHooks builds user-scoped gateway lookups.
func GatewayHooks(st *store.Store, sidecarURL string) gateway.AuthHooks {
	toInfo := func(i functions.Info) gateway.FuncInfo {
		return gateway.FuncInfo{Ref: i.Ref, AuthMode: i.AuthMode, OwnerID: i.OwnerID}
	}
	return gateway.AuthHooks{
		Lookup: func(r *http.Request, name string) (gateway.FuncInfo, bool) {
			// Owner-scoped shorthand: operator's own function wins.
			if tok := auth.Bearer(r.Header.Get("Authorization")); tok != "" {
				if u, ok := st.GetSessionUser(auth.HashSecret(tok)); ok {
					if fn, err := st.GetFunctionOwned(u.ID, name); err == nil && fn.ActiveVersion != "" {
						if _, ok2 := functions.ResolveFull(st, name, sidecarURL); ok2 {
							return toInfo(functions.Info{Ref: mustRef(st, fn, sidecarURL), OwnerID: fn.OwnerID, AuthMode: fn.AuthMode, Slug: fn.Slug}), true
						}
					}
				}
			}
			i, ok := functions.ResolveFull(st, name, sidecarURL)
			if !ok {
				return gateway.FuncInfo{}, false
			}
			return toInfo(i), true
		},
		LookupNS: func(_ *http.Request, owner, name string) (gateway.FuncInfo, bool) {
			i, ok := functions.ResolveNamespaced(st, owner, name, sidecarURL)
			if !ok {
				return gateway.FuncInfo{}, false
			}
			return toInfo(i), true
		},
		LookupSlug: func(_ *http.Request, slug string) (gateway.FuncInfo, bool) {
			i, ok := functions.ResolveBySlug(st, slug, sidecarURL)
			if !ok {
				return gateway.FuncInfo{}, false
			}
			return toInfo(i), true
		},
		CheckKey: func(fnName, key string) bool {
			return CheckKey(st, fnName, key)
		},
		OperatorOf: OperatorOf(st),
	}
}

func mustRef(st *store.Store, fn store.Function, sidecarURL string) runner.FunctionRef {
	i, _ := functions.ResolveFull(st, fn.Name, sidecarURL)
	return i.Ref
}

// CheckKey verifies an API key for fn and touches last_used_at.
func CheckKey(st *store.Store, fnName, key string) bool {
	if key == "" {
		return false
	}
	k, ok := st.FindAPIKey(fnName, auth.HashSecret(key))
	if !ok {
		return false
	}
	st.TouchAPIKey(k.ID)
	return true
}

// --- device flow ---

func RegisterAuthRoutes(mux *http.ServeMux, st *store.Store) {
	mux.HandleFunc("POST /auth/device/start", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DeviceName string `json:"device_name"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&in)
		deviceCode, err := auth.NewDeviceCode()
		if err != nil {
			http.Error(w, "mint failed", http.StatusInternalServerError)
			return
		}
		var userCode string
		for i := 0; i < 5; i++ {
			userCode, err = auth.NewUserCode()
			if err != nil {
				http.Error(w, "mint failed", http.StatusInternalServerError)
				return
			}
			err = st.CreateDevice(auth.HashSecret(deviceCode), userCode, in.DeviceName, time.Now().Add(auth.DeviceTTL))
			if err == nil {
				break
			}
			userCode = ""
		}
		if userCode == "" {
			http.Error(w, "mint failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"device_code": deviceCode,
			"user_code":   userCode,
			"verify_path": "/auth/device?code=" + userCode,
			"expires_in":  int(auth.DeviceTTL.Seconds()),
		})
	})

	mux.HandleFunc("GET /auth/device", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		d, err := st.GetDeviceByUserCode(code)
		if err != nil || expired(d.ExpiresAt) {
			if err == nil {
				st.DeleteDevice(d.CodeHash)
			}
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte("code expired or unknown — run `actions login` again"))
			return
		}
		if d.Status != "pending" {
			_, _ = w.Write([]byte("already verified — return to your terminal"))
			return
		}
		first := st.CountUsers() == 0
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, devicePage(code, first))
	})

	mux.HandleFunc("POST /auth/device/approve", func(w http.ResponseWriter, r *http.Request) {
		var userCode, username, password string
		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			var in struct {
				UserCode string `json:"user_code"`
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&in); err != nil {
				http.Error(w, "bad JSON", http.StatusBadRequest)
				return
			}
			userCode, username, password = in.UserCode, in.Username, in.Password
		} else {
			_ = r.ParseForm()
			userCode = r.FormValue("user_code")
			username = r.FormValue("username")
			password = r.FormValue("password")
		}
		d, err := st.GetDeviceByUserCode(userCode)
		if err != nil || expired(d.ExpiresAt) {
			if err == nil {
				st.DeleteDevice(d.CodeHash)
			}
			http.Error(w, "code expired", http.StatusGone)
			return
		}
		username = strings.ToLower(strings.TrimSpace(username))
		if err := auth.ValidateUsername(username); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var userID string
		if st.CountUsers() == 0 {
			// First verification creates the first account.
			h, err := auth.HashPassword(password)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			u, err := st.CreateUser(username, h)
			if err != nil {
				http.Error(w, "username taken", http.StatusConflict)
				return
			}
			userID = u.ID
		} else {
			u, err := st.CheckUserPassword(username, password, auth.CheckPassword)
			if err != nil {
				http.Error(w, "invalid credentials", http.StatusUnauthorized)
				return
			}
			userID = u.ID
		}
		// Backfill legacy unowned functions to the first user.
		backfillOwner(st, userID)
		if err := st.ApproveDevice(userCode, userID, ""); err != nil {
			http.Error(w, "approve failed", http.StatusInternalServerError)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "application/json") ||
			strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			writeJSON(w, map[string]any{"ok": true})
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body style="font-family:sans-serif;max-width:480px;margin:4em auto">` +
			`<h2>Verified ✓</h2><p>Return to your terminal — login will complete automatically.</p></body></html>`))
	})

	mux.HandleFunc("POST /auth/device/poll", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DeviceCode string `json:"device_code"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&in); err != nil || in.DeviceCode == "" {
			http.Error(w, "device_code required", http.StatusBadRequest)
			return
		}
		d, err := st.GetDeviceByHash(auth.HashSecret(in.DeviceCode))
		if err != nil {
			http.Error(w, "unknown device", http.StatusNotFound)
			return
		}
		if expired(d.ExpiresAt) {
			st.DeleteDevice(d.CodeHash)
			writeJSON(w, map[string]any{"status": "expired"})
			return
		}
		switch d.Status {
		case "denied":
			st.DeleteDevice(d.CodeHash)
			writeJSON(w, map[string]any{"status": "denied"})
		case "approved":
			full, hash, err := auth.NewOperatorToken()
			if err != nil {
				http.Error(w, "mint failed", http.StatusInternalServerError)
				return
			}
			if err := st.CreateSession(hash, d.UserID, time.Now().Add(auth.SessionTTL)); err != nil {
				http.Error(w, "mint failed", http.StatusInternalServerError)
				return
			}
			u, _ := st.GetUserByID(d.UserID)
			st.DeleteDevice(d.CodeHash)
			writeJSON(w, map[string]any{"status": "approved", "token": full, "username": u.Name})
		default:
			writeJSON(w, map[string]any{"status": "pending"})
		}
	})

	mux.HandleFunc("GET /auth/me", RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
		u, ok := CurrentUser(st, r)
		if !ok {
			writeJSON(w, map[string]any{"setup": true})
			return
		}
		writeJSON(w, map[string]any{"id": u.ID, "name": u.Name, "created_at": u.CreatedAt})
	}))

	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		tok := auth.Bearer(r.Header.Get("Authorization"))
		if tok != "" {
			st.DeleteSession(auth.HashSecret(tok))
		}
		writeJSON(w, map[string]any{"ok": true})
	})
}

// backfillOwner claims legacy unowned functions/versions for the first user.
func backfillOwner(st *store.Store, userID string) {
	if st.CountUsers() > 1 {
		return
	}
	fns, err := st.ListFunctions()
	if err != nil {
		return
	}
	for _, f := range fns {
		if f.OwnerID == "" {
			_ = st.UpsertFunctionOwned(userID, f.Name, f.ActiveVersion, f.ConfigTOML, f.Slug, f.AuthMode)
		}
	}
}

func expired(raw string) bool {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return true
	}
	return time.Now().After(t)
}

func devicePage(code string, first bool) string {
	title, hint := "Log in this device", "Enter your username + password to verify this CLI login."
	action := "Log in &amp; verify"
	if first {
		title = "Create your account"
		hint = "No users exist yet — this creates the first account (instance owner)."
		action = "Create account &amp; verify"
	}
	return `<html><body style="font-family:sans-serif;max-width:480px;margin:4em auto">` +
		`<h2>` + title + `</h2><p>` + hint + `</p>` +
		`<p>Device code: <code>` + html.EscapeString(code) + `</code></p>` +
		`<form method="POST" action="/auth/device/approve">` +
		`<input type="hidden" name="user_code" value="` + html.EscapeString(code) + `">` +
		`<p><label>Username<br><input name="username" autocomplete="username" required minlength="2" maxlength="32"></label></p>` +
		`<p><label>Password<br><input name="password" type="password" autocomplete="current-password" required minlength="8"></label></p>` +
		`<p><button type="submit">` + action + `</button></p></form></body></html>`
}

// --- api keys ---

func RegisterKeyRoutes(mux *http.ServeMux, st *store.Store) {
	mux.HandleFunc("POST /keys", RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
		u, ok := CurrentUser(st, r)
		if !ok {
			http.Error(w, "setup required", http.StatusPreconditionRequired)
			return
		}
		var in struct {
			Fn   string `json:"fn"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&in); err != nil || in.Fn == "" {
			http.Error(w, "fn required", http.StatusBadRequest)
			return
		}
		fn, err := st.GetFunction(in.Fn)
		if err != nil {
			http.Error(w, "unknown function", http.StatusNotFound)
			return
		}
		if fn.OwnerID != "" && fn.OwnerID != u.ID {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if fn.OwnerID == "" {
			// Claim legacy function on first key op.
			_ = st.UpsertFunctionOwned(u.ID, fn.Name, fn.ActiveVersion, fn.ConfigTOML, fn.Slug, fn.AuthMode)
		}
		full, prefix, hash, err := auth.NewAPIKey()
		if err != nil {
			http.Error(w, "mint failed", http.StatusInternalServerError)
			return
		}
		k, err := st.CreateAPIKey(u.ID, fn.Name, in.Name, prefix, hash)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"id": k.ID, "fn": k.FnName, "name": k.Name, "prefix": k.Prefix,
			"key": full, "created_at": k.CreatedAt,
		})
	}))

	mux.HandleFunc("GET /keys", RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
		u, ok := CurrentUser(st, r)
		if !ok {
			writeJSON(w, []any{})
			return
		}
		fn := r.URL.Query().Get("fn")
		if fn == "" {
			http.Error(w, "?fn= required", http.StatusBadRequest)
			return
		}
		keys, _ := st.ListAPIKeys(u.ID, fn)
		if keys == nil {
			keys = []store.APIKey{}
		}
		type out struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Prefix     string `json:"prefix"`
			CreatedAt  string `json:"created_at"`
			RevokedAt  string `json:"revoked_at"`
			LastUsedAt string `json:"last_used_at"`
		}
		res := make([]out, 0, len(keys))
		for _, k := range keys {
			res = append(res, out{k.ID, k.Name, k.Prefix, k.CreatedAt, k.RevokedAt, k.LastUsedAt})
		}
		writeJSON(w, res)
	}))

	mux.HandleFunc("POST /keys/", RequireOperator(st, func(w http.ResponseWriter, r *http.Request) {
		u, ok := CurrentUser(st, r)
		if !ok {
			http.Error(w, "setup required", http.StatusPreconditionRequired)
			return
		}
		// POST /keys/<id>/revoke
		rest := strings.TrimPrefix(r.URL.Path, "/keys/")
		id, _, _ := strings.Cut(rest, "/")
		if id == "" || !strings.HasSuffix(r.URL.Path, "/revoke") {
			http.Error(w, "POST /keys/<id>/revoke", http.StatusNotFound)
			return
		}
		if err := st.RevokeAPIKey(u.ID, id); err != nil {
			http.Error(w, "key not found", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	}))
}
