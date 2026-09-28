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
type Registry struct {
	mu    sync.RWMutex
	toks  map[string]map[string]bool // fn -> token set
}

func NewRegistry() *Registry { return &Registry{toks: map[string]map[string]bool{}} }

func (r *Registry) Mint(fn string) string {
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
	for _, set := range r.toks {
		if set[token] {
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
}
