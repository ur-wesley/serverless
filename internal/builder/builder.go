package builder

import "context"

// Request describes one compile job: src dir -> handler image.
// Phase 1: builders are Dockerfiles invoked manually
// (docker build -f builders/builder-ts/Dockerfile <func-dir>).
// Phase 2 automates this in internal/deploy via BuildKit + RustFS.
type Request struct {
	Name     string // function name
	Runtime  string // ts | go | rust | zig
	SrcDir   string // local path or extracted src.zip
	OutImage string // e.g. hello-ts:latest
}

// Builder compiles src into a handler image serving GET /healthz + POST /invoke.
type Builder interface {
	Build(ctx context.Context, req Request) error
}
