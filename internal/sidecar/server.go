// Package sidecar implements $ACTIONS_SIDECAR_URL: KV/Blob/Queue/Log for
// handler binaries. Plain JSON over HTTP, field-compatible with
// proto/actions/v1/sidecar.proto so hand-rolled clients work and future
// ConnectRPC handlers can mount on the same routes.
package sidecar

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"actions/internal/artifacts"
	"actions/internal/bus"
)

// --- KV ---

type KV interface {
	Get(fn, key string) ([]byte, bool)
	Put(fn, key string, value []byte, ttl time.Duration)
	Del(fn, key string)
}

type kvEntry struct {
	value []byte
	exp   time.Time // zero = no expiry
}

type MemKV struct {
	mu   sync.RWMutex
	data map[string]kvEntry
}

func NewMemKV() *MemKV { return &MemKV{data: map[string]kvEntry{}} }

func kvField(fn, key string) string { return fn + "\x00" + key }

func (m *MemKV) Get(fn, key string) ([]byte, bool) {
	m.mu.RLock()
	e, ok := m.data[kvField(fn, key)]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if !e.exp.IsZero() && time.Now().After(e.exp) {
		m.mu.Lock()
		delete(m.data, kvField(fn, key))
		m.mu.Unlock()
		return nil, false
	}
	return e.value, true
}

func (m *MemKV) Put(fn, key string, value []byte, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := kvEntry{value: value}
	if ttl > 0 {
		e.exp = time.Now().Add(ttl)
	}
	m.data[kvField(fn, key)] = e
}

func (m *MemKV) Del(fn, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, kvField(fn, key))
}

// NatsKV persists small KV in JetStream (bucket "actions-kv").
type NatsKV struct {
	kv jetstream.KeyValue
	nc *nats.Conn
}

func dialNATS() *nats.Conn {
	u := os.Getenv("NATS_URL")
	if u == "" {
		return nil
	}
	nc, err := nats.Connect(u, nats.Timeout(3*time.Second))
	if err != nil {
		slog.Warn("nats kv unavailable, using memory", "err", err)
		return nil
	}
	return nc
}

func NewKVFromEnv() (KV, func()) {
	nc := dialNATS()
	if nc == nil {
		return NewMemKV(), func() {}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return NewMemKV(), func() {}
	}
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "actions-kv"})
	if err != nil {
		slog.Warn("nats kv bucket unavailable, using memory", "err", err)
		nc.Close()
		return NewMemKV(), func() {}
	}
	return &NatsKV{kv: kv, nc: nc}, func() { nc.Drain() }
}

func natsKVKey(fn, key string) string { return sanitize(fn) + "." + sanitize(key) }

func sanitize(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "k"
	}
	return b.String()
}

func (n *NatsKV) Get(fn, key string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e, err := n.kv.Get(ctx, natsKVKey(fn, key))
	if err != nil {
		return nil, false
	}
	return unwrapNatsKVValue(e.Value())
}

// unwrapNatsKVValue decodes values written by Put: plain bytes pass through,
// TTL envelopes {"exp":unix,"v":base64} are unwrapped and expiry-checked.
func unwrapNatsKVValue(raw []byte) ([]byte, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return raw, true
	}
	var env struct {
		Exp int64  `json:"exp"`
		V   []byte `json:"v"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Exp == 0 || env.V == nil {
		return raw, true
	}
	if time.Now().Unix() > env.Exp {
		return nil, false
	}
	return env.V, true
}

func (n *NatsKV) Put(fn, key string, value []byte, ttl time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if ttl > 0 {
		// Per-key TTL isn't supported; store expiry envelope instead.
		wrapped, _ := json.Marshal(map[string]any{"exp": time.Now().Add(ttl).Unix(), "v": value})
		_, _ = n.kv.Put(ctx, natsKVKey(fn, key), wrapped)
		return
	}
	_, _ = n.kv.Put(ctx, natsKVKey(fn, key), value)
}

func (n *NatsKV) Del(fn, key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = n.kv.Delete(ctx, natsKVKey(fn, key))
}

// --- Blob ---

type BlobStore interface {
	PutURL(ctx context.Context, fn, key string, expires time.Duration) (string, error)
	GetURL(ctx context.Context, fn, key string, expires time.Duration) (string, error)
}

func BlobKey(fn, key string) string { return "blobs/" + fn + "/" + key }

// LocalBlob points handlers at controlplane transfer endpoints (/blob/ul|dl)
// backed by the artifacts store. Used when RUSTFS_ENDPOINT is unset.
type LocalBlob struct{ ControlURL string }

func (l *LocalBlob) PutURL(_ context.Context, fn, key string, _ time.Duration) (string, error) {
	return l.ControlURL + "/blob/ul?key=" + url.QueryEscape(BlobKey(fn, key)), nil
}

func (l *LocalBlob) GetURL(_ context.Context, fn, key string, _ time.Duration) (string, error) {
	return l.ControlURL + "/blob/dl?key=" + url.QueryEscape(BlobKey(fn, key)), nil
}

// S3Blob returns RustFS presigned URLs.
type S3Blob struct{ S3 *artifacts.S3 }

func (s *S3Blob) PutURL(ctx context.Context, fn, key string, expires time.Duration) (string, error) {
	return s.S3.PresignPut(ctx, BlobKey(fn, key), expires)
}

func (s *S3Blob) GetURL(ctx context.Context, fn, key string, expires time.Duration) (string, error) {
	return s.S3.PresignGet(ctx, BlobKey(fn, key), expires)
}

func NewBlobFromEnv(arts artifacts.Store, controlURL string) BlobStore {
	if s3store, ok := arts.(*artifacts.S3); ok {
		return &S3Blob{S3: s3store}
	}
	return &LocalBlob{ControlURL: controlURL}
}

// --- Logs ---

type LogLine struct {
	Function  string
	Version   string
	RequestID string
	Line      string
	Time      time.Time
}

type LogRing struct {
	mu      sync.Mutex
	entries map[string][]LogLine // per function, last 500
}

func NewLogRing() *LogRing { return &LogRing{entries: map[string][]LogLine{}} }

func (l *LogRing) Append(fn, ver, reqID, line string) {
	slog.Info("fn log", "fn", fn, "ver", ver, "req", reqID, "line", line)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries[fn] = append(l.entries[fn], LogLine{Function: fn, Version: ver, RequestID: reqID, Line: line, Time: time.Now()})
	if len(l.entries[fn]) > 500 {
		l.entries[fn] = l.entries[fn][len(l.entries[fn])-500:]
	}
}

func (l *LogRing) Tail(fn string, n int) []LogLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	all := l.entries[fn]
	if n <= 0 || n > len(all) {
		n = len(all)
	}
	out := make([]LogLine, n)
	copy(out, all[len(all)-n:])
	return out
}

// --- HTTP server ---

type Server struct {
	KV    KV
	Blobs BlobStore
	Bus   bus.Bus
	Logs  *LogRing
	// Tokens enforces per-function bearer auth (nil = open, tests only).
	Tokens *Registry
	// OwnerOf resolves function -> ownerID. Wired by controlplane to avoid
	// importing store here (cycle-safe). Nil = skip owner check (tests/dev).
	OwnerOf func(name string) (string, bool)
	// ListOwn lists functions visible to ownerID (names only for intercom).
	ListOwn func(ownerID string) []string
	// Invoker runs another function for POST /sidecar/invoke. Nil = 501.
	Invoker interface {
		Invoke(ctx context.Context, target string, method, path string, headers, query map[string]string, body []byte) (status int, respHeaders map[string]string, respBody []byte, err error)
	}
}

// authorize checks X-Actions-Token against fn. Nil registry = open.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, fn string) bool {
	if s.Tokens == nil {
		return true
	}
	if fn == "" {
		http.Error(w, "function required", http.StatusBadRequest)
		return false
	}
	tok := r.Header.Get("X-Actions-Token")
	if s.Tokens.Check(fn, tok) {
		return true
	}
	// Intercom: caller may act as target when its policy allows + same owner.
	// KV/blob callers use capability-specific checks below; this fallback
	// keeps backward compat for tests with plain Mint tokens (no policy).
	if caller, ok := s.Tokens.CallerOf(tok); ok && caller != "" && caller != fn {
		// Generic fallback denied here; capability routes call authorizeAs.
		_ = caller
	}
	http.Error(w, "forbidden", http.StatusForbidden)
	return false
}

// authorizeAs checks self OR intercom policy for a capability.
// capability is one of "logs", "invoke", "kv", "blobs".
func (s *Server) authorizeAs(w http.ResponseWriter, r *http.Request, capability, target string) (caller string, ok bool) {
	if s.Tokens == nil {
		return "", true
	}
	tok := r.Header.Get("X-Actions-Token")
	if target == "" {
		http.Error(w, "function required", http.StatusBadRequest)
		return "", false
	}
	if s.Tokens.Check(target, tok) {
		caller, _ = s.Tokens.CallerOf(tok)
		return caller, true
	}
	caller, hasCaller := s.Tokens.CallerOf(tok)
	if !hasCaller {
		http.Error(w, "forbidden", http.StatusForbidden)
		return "", false
	}
	pol, _ := s.Tokens.PolicyOf(tok)
	var list []string
	switch capability {
	case "logs":
		list = pol.AllowLogs
	case "invoke":
		list = pol.AllowInvoke
	case "kv":
		list = pol.AllowKV
	case "blobs":
		list = pol.AllowBlobs
	default:
		http.Error(w, "forbidden", http.StatusForbidden)
		return "", false
	}
	if !Allows(list, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return "", false
	}
	if !s.sameOwner(caller, pol.OwnerID, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return "", false
	}
	return caller, true
}

func (s *Server) sameOwner(caller, callerOwner, target string) bool {
	if s.OwnerOf == nil {
		return true
	}
	targetOwner, ok := s.OwnerOf(target)
	if !ok {
		return false
	}
	// Legacy unowned functions (empty owner) can intercom with each other.
	return callerOwner == targetOwner
}

func readJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "bad JSON", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func b64decode(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /sidecar/kv/get", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			FunctionName string `json:"function_name"`
			Key          string `json:"key"`
		}
		if !readJSON(w, r, 64*1024, &in) {
			return
		}
		if _, ok := s.authorizeKV(w, r, in.FunctionName); !ok {
			return
		}
		v, ok := s.KV.Get(in.FunctionName, in.Key)
		writeJSON(w, map[string]any{"value": base64.StdEncoding.EncodeToString(v), "found": ok})
	})
	mux.HandleFunc("POST /sidecar/kv/put", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			FunctionName string `json:"function_name"`
			Key          string `json:"key"`
			Value        string `json:"value"` // base64
			TTLSeconds   int64  `json:"ttl_seconds"`
		}
		if !readJSON(w, r, 1<<20, &in) {
			return
		}
		if _, ok := s.authorizeKV(w, r, in.FunctionName); !ok {
			return
		}
		v, err := b64decode(in.Value)
		if err != nil {
			http.Error(w, "value must be base64", http.StatusBadRequest)
			return
		}
		s.KV.Put(in.FunctionName, in.Key, v, time.Duration(in.TTLSeconds)*time.Second)
		writeJSON(w, map[string]any{})
	})
	mux.HandleFunc("POST /sidecar/kv/del", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			FunctionName string `json:"function_name"`
			Key          string `json:"key"`
		}
		if !readJSON(w, r, 64*1024, &in) {
			return
		}
		if _, ok := s.authorizeKV(w, r, in.FunctionName); !ok {
			return
		}
		s.KV.Del(in.FunctionName, in.Key)
		writeJSON(w, map[string]any{})
	})
	mux.HandleFunc("POST /sidecar/blob/put-url", func(w http.ResponseWriter, r *http.Request) {
		s.blobURL(w, r, true)
	})
	mux.HandleFunc("POST /sidecar/blob/get-url", func(w http.ResponseWriter, r *http.Request) {
		s.blobURL(w, r, false)
	})
	mux.HandleFunc("POST /sidecar/queue/publish", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Topic        string `json:"topic"`
			Message      string `json:"message"` // base64
			FunctionName string `json:"function_name"`
		}
		if !readJSON(w, r, 4<<20, &in) {
			return
		}
		if s.Tokens != nil {
			tok := r.Header.Get("X-Actions-Token")
			if !s.Tokens.ValidAny(tok) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			// When the caller identifies itself, the token must belong to
			// that function (prevents one function impersonating another).
			if in.FunctionName != "" && !s.Tokens.Check(in.FunctionName, tok) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		if in.Topic == "" {
			http.Error(w, "topic required", http.StatusBadRequest)
			return
		}
		msg, err := b64decode(in.Message)
		if err != nil {
			http.Error(w, "message must be base64", http.StatusBadRequest)
			return
		}
		if err := s.Bus.Publish(r.Context(), in.Topic, msg); err != nil {
			http.Error(w, fmt.Sprintf("publish: %v", err), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{})
	})
	mux.HandleFunc("POST /sidecar/log/append", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			FunctionName string `json:"function_name"`
			Version      string `json:"version"`
			RequestID    string `json:"request_id"`
			Line         string `json:"line"`
		}
		if !readJSON(w, r, 64*1024, &in) {
			return
		}
		if !s.authorize(w, r, in.FunctionName) {
			return
		}
		s.Logs.Append(in.FunctionName, in.Version, in.RequestID, in.Line)
		writeJSON(w, map[string]any{})
	})
	// Intercom: tail another function's logs when allow_logs + same owner.
	mux.HandleFunc("POST /sidecar/logs/tail", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TargetFunction string `json:"target_function"`
			FunctionName   string `json:"function_name"` // alias
			Limit          int    `json:"limit"`
		}
		if !readJSON(w, r, 64*1024, &in) {
			return
		}
		target := in.TargetFunction
		if target == "" {
			target = in.FunctionName
		}
		caller, ok := s.authorizeAs(w, r, "logs", target)
		if !ok {
			return
		}
		_ = caller
		n := in.Limit
		if n <= 0 || n > 500 {
			n = 100
		}
		writeJSON(w, map[string]any{"lines": s.Logs.Tail(target, n)})
	})
	// Intercom: list own functions (names only) for dashboard discovery.
	// Any valid function token may call; results are scoped to caller owner.
	mux.HandleFunc("POST /sidecar/functions/list", func(w http.ResponseWriter, r *http.Request) {
		if s.Tokens != nil && !s.Tokens.ValidAny(r.Header.Get("X-Actions-Token")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		names := []string{}
		if s.ListOwn != nil {
			caller, _ := s.Tokens.CallerOf(r.Header.Get("X-Actions-Token"))
			pol, _ := s.Tokens.PolicyOf(r.Header.Get("X-Actions-Token"))
			_ = caller
			if got := s.ListOwn(pol.OwnerID); got != nil {
				names = got
			}
		}
		writeJSON(w, map[string]any{"functions": names})
	})
	// Intercom: invoke another function when allow_invoke + same owner.
	mux.HandleFunc("POST /sidecar/invoke", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TargetFunction string            `json:"target_function"`
			FunctionName   string            `json:"function_name"` // alias
			Method         string            `json:"method"`
			Path           string            `json:"path"`
			Headers        map[string]string `json:"headers"`
			Query          map[string]string `json:"query"`
			Body           string            `json:"body"` // base64
		}
		if !readJSON(w, r, 4<<20, &in) {
			return
		}
		target := in.TargetFunction
		if target == "" {
			target = in.FunctionName
		}
		if _, ok := s.authorizeAs(w, r, "invoke", target); !ok {
			return
		}
		if s.Invoker == nil {
			http.Error(w, "invoke unavailable", http.StatusNotImplemented)
			return
		}
		method := in.Method
		if method == "" {
			method = "GET"
		}
		p := in.Path
		if p == "" {
			p = "/"
		}
		raw, err := b64decode(in.Body)
		if err != nil {
			http.Error(w, "body must be base64", http.StatusBadRequest)
			return
		}
		status, respHeaders, respBody, err := s.Invoker.Invoke(r.Context(), target, method, p, in.Headers, in.Query, raw)
		if err != nil {
			http.Error(w, "invoke: "+err.Error(), http.StatusBadGateway)
			return
		}
		if respHeaders == nil {
			respHeaders = map[string]string{}
		}
		writeJSON(w, map[string]any{
			"status":  status,
			"headers": respHeaders,
			"body":    base64.StdEncoding.EncodeToString(respBody),
		})
	})
}

// authorizeKV allows self or allow_kv intercom.
func (s *Server) authorizeKV(w http.ResponseWriter, r *http.Request, target string) (string, bool) {
	if s.Tokens == nil {
		return "", true
	}
	tok := r.Header.Get("X-Actions-Token")
	if target == "" {
		http.Error(w, "function required", http.StatusBadRequest)
		return "", false
	}
	if s.Tokens.Check(target, tok) {
		caller, _ := s.Tokens.CallerOf(tok)
		return caller, true
	}
	return s.authorizeAs(w, r, "kv", target)
}

// authorizeBlob allows self or allow_blobs intercom.
func (s *Server) authorizeBlob(w http.ResponseWriter, r *http.Request, target string) (string, bool) {
	if s.Tokens == nil {
		return "", true
	}
	tok := r.Header.Get("X-Actions-Token")
	if target == "" {
		http.Error(w, "function required", http.StatusBadRequest)
		return "", false
	}
	if s.Tokens.Check(target, tok) {
		caller, _ := s.Tokens.CallerOf(tok)
		return caller, true
	}
	return s.authorizeAs(w, r, "blobs", target)
}

func (s *Server) blobURL(w http.ResponseWriter, r *http.Request, put bool) {	var in struct {
		FunctionName   string `json:"function_name"`
		Key            string `json:"key"`
		ExpiresSeconds int64  `json:"expires_seconds"`
	}
	if !readJSON(w, r, 64*1024, &in) {
		return
	}
	if in.Key == "" {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}
	if _, ok := s.authorizeBlob(w, r, in.FunctionName); !ok {
		return
	}
	exp := time.Duration(in.ExpiresSeconds) * time.Second
	if exp <= 0 || exp > time.Hour {
		exp = 15 * time.Minute
	}
	var (
		u   string
		err error
	)
	if put {
		u, err = s.Blobs.PutURL(r.Context(), in.FunctionName, in.Key, exp)
	} else {
		u, err = s.Blobs.GetURL(r.Context(), in.FunctionName, in.Key, exp)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"url": u})
}

// RegisterTransferRoutes serves the LocalBlob byte transfer behind the
// presigned-style URLs: PUT /blob/ul?key=blobs/<fn>/<key>,
// GET /blob/dl?key=blobs/<fn>/<key>. Mounted on the control plane.
// tokens may be nil (open); otherwise X-Actions-Token must match the fn
// embedded in the key, or belong to an intercom-allowed caller (allow_blobs
// + same owner when ownerOf is set).
func RegisterTransferRoutes(mux *http.ServeMux, arts artifacts.Store, tokens *Registry) {
	RegisterTransferRoutesWithOwner(mux, arts, tokens, nil)
}

// RegisterTransferRoutesWithOwner adds same-owner intercom for blobs.
func RegisterTransferRoutesWithOwner(mux *http.ServeMux, arts artifacts.Store, tokens *Registry, ownerOf func(name string) (string, bool)) {
	check := func(w http.ResponseWriter, r *http.Request, key string) bool {
		fn, _, _ := strings.Cut(strings.TrimPrefix(key, "blobs/"), "/")
		if tokens == nil {
			return true
		}
		tok := r.Header.Get("X-Actions-Token")
		if fn == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return false
		}
		if tokens.Check(fn, tok) {
			return true
		}
		caller, ok := tokens.CallerOf(tok)
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return false
		}
		pol, _ := tokens.PolicyOf(tok)
		if !Allows(pol.AllowBlobs, fn) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return false
		}
		if ownerOf != nil {
			targetOwner, ok := ownerOf(fn)
			if !ok || targetOwner != pol.OwnerID {
				http.Error(w, "forbidden", http.StatusForbidden)
				return false
			}
			_ = caller
		}
		return true
	}
	mux.HandleFunc("PUT /blob/ul", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if !strings.HasPrefix(key, "blobs/") || key == "" {
			http.Error(w, "bad key", http.StatusBadRequest)
			return
		}
		if !check(w, r, key) {
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		if err := arts.Put(r.Context(), key, data); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /blob/dl", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if !strings.HasPrefix(key, "blobs/") || key == "" {
			http.Error(w, "bad key", http.StatusBadRequest)
			return
		}
		if !check(w, r, key) {
			return
		}
		data, err := arts.Get(r.Context(), key)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
	})
	mux.HandleFunc("DELETE /blob/del", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if !strings.HasPrefix(key, "blobs/") || key == "" {
			http.Error(w, "bad key", http.StatusBadRequest)
			return
		}
		if !check(w, r, key) {
			return
		}
		if err := arts.Delete(r.Context(), key); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
