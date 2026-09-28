package sidecar

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Registry binds bearer tokens to functions so a compromised handler
// cannot read another function's KV/blobs/logs. Runner mints one token
// per instance (env ACTIONS_SIDECAR_TOKEN); sidecar checks it against the
// function_name (or key) on every call. Nil *Registry disables checks
// (unit tests only — control plane always wires one).
//
// Intercom: a function may access another function's data only when its
// own actions.toml opt-in (allow_logs/allow_invoke/allow_kv/allow_blobs)
// names the target (or "*" for all own functions) AND both functions share
// the same owner (enforced by the sidecar server via the store).
type Registry struct {
	mu    sync.RWMutex
	toks  map[string]map[string]bool // fn -> token set
	byTok map[string]*entry           // token -> caller + policy
}

type Policy struct {
	OwnerID     string
	AllowLogs   []string
	AllowInvoke []string
	AllowKV     []string
	AllowBlobs  []string
}

type entry struct {
	fn     string
	policy Policy
}

func NewRegistry() *Registry { return &Registry{toks: map[string]map[string]bool{}, byTok: map[string]*entry{}} }

func (r *Registry) Mint(fn string) string {
	return r.MintWithPolicy(fn, "", nil, nil, nil, nil)
}

func (r *Registry) MintWithPolicy(fn, ownerID string, allowLogs, allowInvoke, allowKV, allowBlobs []string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	tok := hex.EncodeToString(b[:])
	r.mu.Lock()
	defer r.mu.Unlock()
	set := r.toks[fn]
	if set == nil {
		set = map[string]bool{}
		r.toks[fn] = set
	}
	set[tok] = true
	r.byTok[tok] = &entry{fn: fn, policy: Policy{
		OwnerID: ownerID, AllowLogs: allowLogs, AllowInvoke: allowInvoke,
		AllowKV: allowKV, AllowBlobs: allowBlobs,
	}}
	return tok
}

// Check reports whether token is valid for fn.
func (r *Registry) Check(fn, token string) bool {
	if fn == "" || token == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.toks[fn][token]
}

// ValidAny reports whether token belongs to any function (queue publish).
func (r *Registry) ValidAny(token string) bool {
	if token == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byTok[token]
	if ok {
		return true
	}
	for _, set := range r.toks {
		if set[token] {
			return true
		}
	}
	return false
}

// CallerOf resolves a token to its owning function.
func (r *Registry) CallerOf(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.byTok[token]; ok {
		return e.fn, true
	}
	// Fallback for tokens minted before policy tracking (tests).
	for fn, set := range r.toks {
		if set[token] {
			return fn, true
		}
	}
	return "", false
}

// PolicyOf returns the intercom policy attached to a token.
func (r *Registry) PolicyOf(token string) (Policy, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.byTok[token]; ok {
		return e.policy, true
	}
	for fn, set := range r.toks {
		if set[token] {
			return Policy{}, true
		}
		_ = fn
	}
	return Policy{}, false
}

// Allows reports whether an allow-list names target ("*" = all).
func Allows(list []string, target string) bool {
	for _, n := range list {
		if n == "*" || n == target {
			return true
		}
	}
	return false
}

func (r *Registry) Revoke(fn, token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if set := r.toks[fn]; set != nil {
		delete(set, token)
		if len(set) == 0 {
			delete(r.toks, fn)
		}
	}
	delete(r.byTok, token)
}
