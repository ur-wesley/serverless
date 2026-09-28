package runner

import (
	"context"
	"time"
)

// Defaults from idea.md: 10s timeout (max 60s), 256MB, 60s idle TTL.
const (
	DefaultTimeout = 10 * time.Second
	MaxTimeout     = 60 * time.Second
	DefaultMemory  = 256 // MB
	IdleTTL        = 60 * time.Second
)

// FunctionRef identifies one versioned function and how to run it.
type FunctionRef struct {
	Name         string
	Version      string
	Image        string // handler container image (Phase 1); digest pinning in Phase 2
	Timeout      time.Duration
	MemoryMB     int
	AllowEgress  bool
	SidecarURL   string
}

// InvokeResponse mirrors InvokeResponse protojson: body is base64 on the wire.
type InvokeResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"` // base64-encoded handler output
}

// Backend runs handler binaries and proxies /invoke. Swappable without
// touching gateway/scheduler (Firecracker explicitly out of scope).
type Backend interface {
	Invoke(ctx context.Context, ref FunctionRef, invokeRequestJSON []byte) (*InvokeResponse, error)
	Close()
}

// Config tunes the docker-runsc backend.
type Config struct {
	// Network for default (egress-allowed or dev) containers.
	Network string
	// SandboxNet, when set, isolates !AllowEgress functions on this
	// pre-created --internal network. They are invoked via container DNS
	// (no published ports), so this only works when the control plane
	// itself runs in a container attached to SandboxNet (compose).
	// Unset (dev) = warn-only, shared-network mode.
	SandboxNet string
	// HostGW is the dialable address for published handler ports
	// (default 127.0.0.1; compose sets host.docker.internal).
	HostGW string
	// Tokens mints per-instance sidecar credentials (nil = none).
	Tokens interface {
		Mint(fn string) string
		Revoke(fn, token string)
	}
	// Arts backs image reload after daemon restarts (nil = skip).
	Arts interface {
		Get(ctx context.Context, key string) ([]byte, error)
	}
}

func ClampTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultTimeout
	}
	if d > MaxTimeout {
		return MaxTimeout
	}
	return d
}
