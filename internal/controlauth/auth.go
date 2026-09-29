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

	"github.com/ur-wesley/oort/internal/auth"
	"github.com/ur-wesley/oort/internal/functions"
	"github.com/ur-wesley/oort/internal/gateway"
	"github.com/ur-wesley/oort/internal/runner"
	"github.com/ur-wesley/oort/internal/store"
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
// The key's owner must match the function's owner (legacy unowned functions
// accept legacy unowned keys) so same-named functions across owners can't
// reuse each other's keys.
func CheckKey(st *store.Store, fnName, key string) bool {
	if key == "" {
		return false
	}
	k, ok := st.FindAPIKey(fnName, auth.HashSecret(key))
	if !ok {
		return false
	}
	if fn, err := st.GetFunction(fnName); err == nil {
		if fn.OwnerID != "" && k.OwnerID != "" && fn.OwnerID != k.OwnerID {
			return false
		}
		if fn.OwnerID != "" && k.OwnerID == "" {
			return false
		}
	}
	st.TouchAPIKey(k.ID)
	return true
}

// FindAPIKeyScoped locates a non-revoked key for an owner+function pair.
func FindAPIKeyScoped(st *store.Store, ownerID, fnName, keyHash string) (store.APIKey, bool) {
	k, ok := st.FindAPIKey(fnName, keyHash)
	if !ok {
		return store.APIKey{}, false
	}
	if ownerID != "" && k.OwnerID != "" && k.OwnerID != ownerID {
		return store.APIKey{}, false
	}
	return k, true
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
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusGone)
			_, _ = fmt.Fprint(w, deviceStatusPage(statusPageArgs{
				Kind:    "expired",
				Title:   "Link expired",
				Heading: "This login link expired",
				Message: "Device codes are valid for 10 minutes. Run `oort login` again in your terminal to get a fresh code.",
			}))
			return
		}
		if d.Status != "pending" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, deviceStatusPage(statusPageArgs{
				Kind:    "info",
				Title:   "Already verified",
				Heading: "Already verified",
				Message: "This device was already approved. Return to your terminal — login will complete automatically.",
			}))
			return
		}
		first := st.CountUsers() == 0
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, deviceLoginPage(code, first, r.URL.Query().Get("error"), ""))
	})

	mux.HandleFunc("POST /auth/device/approve", func(w http.ResponseWriter, r *http.Request) {
		var userCode, username, password string
		isJSON := strings.Contains(r.Header.Get("Content-Type"), "application/json")
		if isJSON {
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
		// renderForm re-displays the styled browser form with an inline
		// error banner (preserving the typed username). JSON callers get
		// the original plain-text status codes.
		renderForm := func(msg string, status int, first bool, code, preservedUser string) {
			if isJSON {
				http.Error(w, msg, status)
				return
			}
			if code == "" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, deviceStatusPage(statusPageArgs{
					Kind:    "expired",
					Title:   "Link expired",
					Heading: "This login link expired",
					Message: msg + " Run `oort login` again for a fresh code.",
				}))
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, deviceLoginPage(code, first, msg, preservedUser))
		}
		if strings.TrimSpace(userCode) == "" {
			if isJSON {
				http.Error(w, "user_code required", http.StatusBadRequest)
			} else {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, deviceStatusPage(statusPageArgs{
					Kind:    "error",
					Title:   "Missing code",
					Heading: "Verification code missing",
					Message: "Open this page from the link printed by `oort login` so the device code is included.",
				}))
			}
			return
		}
		d, err := st.GetDeviceByUserCode(userCode)
		if err != nil || expired(d.ExpiresAt) {
			if err == nil {
				st.DeleteDevice(d.CodeHash)
			}
			if isJSON {
				http.Error(w, "code expired", http.StatusGone)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusGone)
			_, _ = fmt.Fprint(w, deviceStatusPage(statusPageArgs{
				Kind:    "expired",
				Title:   "Link expired",
				Heading: "This login link expired",
				Message: "This code is unknown or older than 10 minutes. Run `oort login` again for a fresh code.",
			}))
			return
		}
		first := st.CountUsers() == 0
		username = strings.ToLower(strings.TrimSpace(username))
		if err := auth.ValidateUsername(username); err != nil {
			renderForm(err.Error(), http.StatusBadRequest, first, userCode, username)
			return
		}
		var userID string
		if first {
			// First verification creates the first account.
			h, err := auth.HashPassword(password)
			if err != nil {
				renderForm(err.Error(), http.StatusBadRequest, true, userCode, username)
				return
			}
			u, err := st.CreateUser(username, h)
			if err != nil {
				renderForm("That username is already taken — pick another one.", http.StatusConflict, true, userCode, username)
				return
			}
			userID = u.ID
		} else {
			u, err := st.CheckUserPassword(username, password, auth.CheckPassword)
			if err != nil {
				renderForm("Invalid username or password. Check caps lock and try again.", http.StatusUnauthorized, false, userCode, username)
				return
			}
			userID = u.ID
		}
		// Backfill legacy unowned functions to the first user.
		backfillOwner(st, userID)
		if err := st.ApproveDevice(userCode, userID, ""); err != nil {
			if isJSON {
				http.Error(w, "approve failed", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, deviceStatusPage(statusPageArgs{
				Kind:    "error",
				Title:   "Something went wrong",
				Heading: "Couldn't verify this device",
				Message: "Please go back and try again. If it keeps failing, run `oort login` for a fresh code.",
			}))
			return
		}
		if isJSON ||
			strings.Contains(r.Header.Get("Accept"), "application/json") {
			writeJSON(w, map[string]any{"ok": true})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, deviceStatusPage(statusPageArgs{
			Kind:    "success",
			Title:   "Verified",
			Heading: "Verified — you're in",
			Message: "Return to your terminal — login will complete automatically. You can close this tab.",
		}))
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

type statusPageArgs struct {
	Kind    string // success | expired | info | error
	Title   string
	Heading string
	Message string
}

// loginShell wraps body in a clean, dark, responsive layout. No external
// assets — single inline stylesheet so the control plane serves it as-is.
func loginShell(title, body string) string {
	return `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<meta name="color-scheme" content="dark">` +
		`<title>` + html.EscapeString(title) + ` — Oort</title><style>` +
		`:root{color-scheme:dark;--bg:#0b0f14;--card:#141b25;--border:#26303d;` +
		`--text:#e6edf3;--muted:#8b949e;--accent:#2f81f7;--accent-h:#1f6feb;` +
		`--danger-bg:#3d1a1e;--danger-bd:#7a2e35;--danger-tx:#ffb4ab;` +
		`--ok-bg:#12291c;--ok-bd:#1f5c38;--ok-tx:#7ee2a8;` +
		`--warn-bg:#33230a;--warn-bd:#7a5410;--warn-tx:#ffce59;` +
		`--info-bg:#12233a;--info-bd:#24507e;--info-tx:#9ecbff}` +
		`*{box-sizing:border-box}body{margin:0;background:radial-gradient(1200px 600px at 50% -10%,#16202e 0%,var(--bg) 55%) fixed,var(--bg);` +
		`color:var(--text);font:16px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Inter,Roboto,Helvetica,Arial,sans-serif;` +
		`-webkit-font-smoothing:antialiased}` +
		`.wrap{max-width:460px;margin:0 auto;padding:56px 20px 48px}` +
		`.brand{display:flex;align-items:center;gap:10px;margin-bottom:20px}` +
		`.dot{width:11px;height:11px;border-radius:50%;background:var(--accent);box-shadow:0 0 14px var(--accent)}` +
		`.brand b{font-size:15px;letter-spacing:.02em}.brand span{color:var(--muted);font-size:13px}` +
		`.card{background:var(--card);border:1px solid var(--border);border-radius:14px;padding:28px;box-shadow:0 18px 50px rgba(0,0,0,.45)}` +
		`h1{font-size:22px;line-height:1.25;margin:0 0 6px}.sub{color:var(--muted);font-size:14px;margin:0 0 18px}` +
		`.alert{border-radius:10px;padding:11px 13px;font-size:14px;margin:0 0 16px;border:1px solid var(--danger-bd);background:var(--danger-bg);color:var(--danger-tx)}` +
		`.code-row{display:flex;align-items:center;gap:10px;background:#0d1117;border:1px dashed var(--border);border-radius:10px;padding:10px 12px;margin:0 0 18px}` +
		`.code-row code{font:700 15px ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;letter-spacing:.06em}` +
		`.code-row button{margin-left:auto;background:#1c2532;color:var(--text);border:1px solid var(--border);border-radius:8px;padding:6px 10px;font-size:12.5px;cursor:pointer}` +
		`.code-row button:hover{border-color:var(--accent)}` +
		`label{display:block;font-size:13.5px;font-weight:600;margin:14px 0 6px}` +
		`input{width:100%;background:#0d1117;border:1px solid #30363d;color:var(--text);border-radius:9px;padding:11px 12px;font-size:15px}` +
		`input:focus{outline:2px solid var(--accent);outline-offset:1px;border-color:var(--accent)}` +
		`.hint{color:var(--muted);font-size:12.5px;margin:5px 0 0;font-weight:400}` +
		`.btn{width:100%;margin-top:20px;background:var(--accent);border:0;color:#fff;font-weight:650;font-size:15px;border-radius:9px;padding:12px;cursor:pointer}` +
		`.btn:hover{background:var(--accent-h)}.btn:disabled{opacity:.6;cursor:wait}` +
		`.meta{color:var(--muted);font-size:13px;margin:16px 0 0}.meta code{font-size:12.5px}` +
		`.status{display:flex;gap:12px;align-items:flex-start;border-radius:10px;padding:13px 14px;font-size:14px;margin:0 0 6px;border:1px solid}` +
		`.status .ic{font-size:18px;line-height:1.3}.status.ok{background:var(--ok-bg);border-color:var(--ok-bd);color:var(--ok-tx)}` +
		`.status.expired{background:var(--warn-bg);border-color:var(--warn-bd);color:var(--warn-tx)}` +
		`.status.info{background:var(--info-bg);border-color:var(--info-bd);color:var(--info-tx)}` +
		`.status.error{background:var(--danger-bg);border-color:var(--danger-bd);color:var(--danger-tx)}` +
		`.foot{color:var(--muted);font-size:12.5px;text-align:center;margin:18px 0 0}` +
		`a{color:#6ea8fe}@media(max-width:520px){.wrap{padding-top:32px}.card{padding:22px}}` +
		`</style></head><body><main class="wrap">` +
		`<div class="brand"><span class="dot" aria-hidden="true"></span><b>Oort</b><span>device verification</span></div>` +
		`<div class="card">` + body + `</div>` +
		`<p class="foot">Code expires 10 minutes after <code>oort login</code> &middot; never share your password</p>` +
		`</main><script>` +
		`var f=document.getElementById("login-form"),b=document.getElementById("login-btn");` +
		`if(f&&b){f.addEventListener("submit",function(){b.disabled=true;b.textContent="Verifying…";});}` +
		`function copyCode(){var el=document.getElementById("ucode");if(!el)return;` +
		`var t=el.textContent||"";if(navigator.clipboard){navigator.clipboard.writeText(t);}` +
		`var btn=document.getElementById("copy-btn");if(btn){btn.textContent="Copied";setTimeout(function(){btn.textContent="Copy";},1500);}}` +
		`</script></body></html>`
}

// deviceLoginPage renders the username/password form. errMsg (already
// user-friendly) is shown as an inline banner; username is preserved.
func deviceLoginPage(code string, first bool, errMsg, username string) string {
	title, hint, action := "Log in this device", "Enter your username + password to verify this CLI login.", "Log in & verify"
	if first {
		title = "Create your account"
		hint = "No users exist yet — this creates the first account (instance owner)."
		action = "Create account & verify"
	}
	var sb strings.Builder
	sb.WriteString(`<h1>` + html.EscapeString(title) + `</h1>`)
	sb.WriteString(`<p class="sub">` + html.EscapeString(hint) + `</p>`)
	if strings.TrimSpace(errMsg) != "" {
		sb.WriteString(`<div class="alert" role="alert">` + html.EscapeString(errMsg) + `</div>`)
	}
	sb.WriteString(`<div class="code-row" title="Device code from your terminal">` +
		`<span aria-hidden="true">⌁</span><code id="ucode">` + html.EscapeString(code) + `</code>` +
		`<button type="button" id="copy-btn" onclick="copyCode()">Copy</button></div>`)
	sb.WriteString(`<form id="login-form" method="POST" action="/auth/device/approve">`)
	sb.WriteString(`<input type="hidden" name="user_code" value="` + html.EscapeString(code) + `">`)
	sb.WriteString(`<label for="username">Username</label>`)
	sb.WriteString(`<input id="username" name="username" autocomplete="username" required minlength="2" maxlength="32" pattern="[A-Za-z0-9-_]{2,32}" value="` + html.EscapeString(username) + `">`)
	sb.WriteString(`<p class="hint">2–32 chars: lowercase letters, numbers, - or _.</p>`)
	sb.WriteString(`<label for="password">Password</label>`)
	autocomplete := "current-password"
	if first {
		autocomplete = "new-password"
	}
	sb.WriteString(`<input id="password" name="password" type="password" autocomplete="` + autocomplete + `" required minlength="8">`)
	if first {
		sb.WriteString(`<p class="hint">Minimum 8 characters — this becomes the owner password.</p>`)
	} else {
		sb.WriteString(`<p class="hint">The password you used with <code>actions login</code> before.</p>`)
	}
	sb.WriteString(`<button class="btn" id="login-btn" type="submit">` + html.EscapeString(action) + `</button>`)
	sb.WriteString(`</form>`)
	sb.WriteString(`<p class="meta">Wrong code? Run <code>actions login</code> again for a fresh link.</p>`)
	return loginShell(title, sb.String())
}

// deviceStatusPage renders expired / verified / error outcomes.
func deviceStatusPage(a statusPageArgs) string {
	icon := "ℹ"
	switch a.Kind {
	case "success":
		icon = "✓"
	case "expired":
		icon = "⏳"
	case "error":
		icon = "⚠"
	}
	body := `<div class="status ` + html.EscapeString(a.Kind) + `"><span class="ic" aria-hidden="true">` + icon + `</span>` +
		`<div><strong>` + html.EscapeString(a.Heading) + `</strong><br>` + html.EscapeString(a.Message) + `</div></div>`
	return loginShell(a.Title, `<h1>`+html.EscapeString(a.Heading)+`</h1><p class="sub">`+html.EscapeString(a.Message)+`</p>`+body)
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
