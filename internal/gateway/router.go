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

	"github.com/ur-wesley/oort/internal/runner"
)

// Wire format (protojson-compatible, hand-rollable):
//
//	InvokeRequest  {event:{method,path,headers,query,body:<base64>}, ctx:{...}}
//	InvokeResponse {status, headers, body:<base64>}
//
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

// FuncInfo carries the runnable ref plus auth/identity for enforcement.
type FuncInfo struct {
	Ref      runner.FunctionRef
	AuthMode string // public|key|private (empty = public)
	OwnerID  string
}

// AuthHooks wires user-scoped resolution + key/operator checks.
// Lookups are request-aware so the control plane can scope legacy
// single-segment names to the caller's own namespace.
type AuthHooks struct {
	Lookup     func(r *http.Request, name string) (FuncInfo, bool)
	LookupNS   func(r *http.Request, owner, name string) (FuncInfo, bool)
	LookupSlug func(r *http.Request, slug string) (FuncInfo, bool)
	CheckKey   func(fnName, key string) bool
	OperatorOf func(r *http.Request) (string, bool) // userID when operator-authed
}

func NewHandler(invoker interface {
	Invoke(ctx context.Context, ref runner.FunctionRef, invokeRequestJSON []byte) (*runner.InvokeResponse, error)
}, lookup Lookup) http.Handler {
	return NewHandlerWithAuth(invoker, AuthHooks{
		Lookup: func(_ *http.Request, name string) (FuncInfo, bool) {
			ref, ok := lookup(name)
			return FuncInfo{Ref: ref, AuthMode: "public"}, ok
		},
	})
}

// NewHandlerWithAuth enforces auth_mode per function:
//   - public: open
//   - key: requires x-api-key / ?key= valid for that function,
//     or operator token owning the function
//   - private: requires operator token owning the function
//
// Routes:
//   - /f/<name>/...         legacy + own-namespace shorthand
//   - /f/<owner>/<name>/... explicit namespace
//   - /s/<slug>/...         random public slug
func NewHandlerWithAuth(invoker interface {
	Invoke(ctx context.Context, ref runner.FunctionRef, invokeRequestJSON []byte) (*runner.InvokeResponse, error)
}, hooks AuthHooks) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info, rest, ok := resolveRoute(r, hooks)
		if !ok {
			http.Error(w, "unknown function", http.StatusNotFound)
			return
		}
		if !authorizeInvoke(w, r, info, hooks) {
			return
		}
		invokeWith(w, r, invoker, info.Ref, rest, start)
	})
}

// resolveRoute maps the URL to (info, rest-path, ok).
func resolveRoute(r *http.Request, hooks AuthHooks) (FuncInfo, string, bool) {
	p := r.URL.Path
	if strings.HasPrefix(p, "/s/") {
		slug, rest := splitSlugPath(p)
		if slug == "" || hooks.LookupSlug == nil {
			return FuncInfo{}, "", false
		}
		info, ok := hooks.LookupSlug(r, slug)
		if !ok {
			return FuncInfo{}, "", false
		}
		return info, rest, true
	}
	owner, name, rest, namespaced := splitFnPathNS(p)
	if !namespaced {
		single, srest := splitFnPath(p)
		if single == "" || hooks.Lookup == nil {
			return FuncInfo{}, "", false
		}
		info, ok := hooks.Lookup(r, single)
		if !ok {
			return FuncInfo{}, "", false
		}
		return info, srest, true
	}
	// Two-or-more segments: try explicit namespace first.
	if hooks.LookupNS != nil {
		if info, ok := hooks.LookupNS(r, owner, name); ok {
			return info, rest, true
		}
	}
	// Fallback: first segment is the function, rest includes second segment.
	if hooks.Lookup == nil {
		return FuncInfo{}, "", false
	}
	info, ok := hooks.Lookup(r, owner)
	if !ok {
		return FuncInfo{}, "", false
	}
	if rest != "" {
		return info, name + "/" + rest, true
	}
	return info, name, true
}

func authorizeInvoke(w http.ResponseWriter, r *http.Request, info FuncInfo, hooks AuthHooks) bool {
	mode := strings.ToLower(info.AuthMode)
	if mode == "" {
		mode = "public"
	}
	opID, isOp := "", false
	if hooks.OperatorOf != nil {
		opID, isOp = hooks.OperatorOf(r)
	}
	owns := isOp && opID != "" && info.OwnerID != "" && opID == info.OwnerID
	// Unowned legacy functions: any operator counts as owner for private/key.
	if isOp && info.OwnerID == "" {
		owns = true
	}
	switch mode {
	case "key":
		if owns {
			return true
		}
		key := r.Header.Get("x-api-key")
		if key == "" {
			key = r.URL.Query().Get("key")
		}
		if key != "" && hooks.CheckKey != nil && hooks.CheckKey(info.Ref.Name, key) {
			return true
		}
		http.Error(w, "api key required", http.StatusUnauthorized)
		return false
	case "private":
		if owns {
			return true
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	default:
		return true
	}
}

func invokeWith(w http.ResponseWriter, r *http.Request, invoker interface {
	Invoke(ctx context.Context, ref runner.FunctionRef, invokeRequestJSON []byte) (*runner.InvokeResponse, error)
}, ref runner.FunctionRef, rest string, start time.Time) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	headers := map[string]string{}
	for k := range r.Header {
		lk := strings.ToLower(k)
		headers[lk] = r.Header.Get(k)
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
		slog.Warn("invoke failed", "fn", ref.Name, "method", r.Method, "path", r.URL.Path, "dur_ms", time.Since(start).Milliseconds(), "err", err)
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
	slog.Info("invoke", "fn", ref.Name, "method", r.Method, "path", r.URL.Path, "status", status, "dur_ms", time.Since(start).Milliseconds())
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

// splitFnPathNS turns /f/alice/hello/a into (alice, hello, a).
// ok=false when fewer than 1 segment.
func splitFnPathNS(p string) (owner, name, rest string, ok bool) {
	trimmed := strings.TrimPrefix(p, "/f/")
	if trimmed == p || trimmed == "" {
		return "", "", "", false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 1 {
		return parts[0], "", "", false // single-segment: not namespaced
	}
	rest = strings.Join(parts[2:], "/")
	return parts[0], parts[1], rest, true
}

// splitSlugPath turns /s/abc12345/a/b into ("abc12345", "a/b").
func splitSlugPath(p string) (string, string) {
	trimmed := strings.TrimPrefix(p, "/s/")
	if trimmed == p {
		return "", ""
	}
	slug, rest, _ := strings.Cut(trimmed, "/")
	return slug, rest
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
