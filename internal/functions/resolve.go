// Package functions resolves a function name to a runnable FunctionRef
// from the SQLite registry. Shared by the gateway router and the scheduler
// so HTTP, cron and queue triggers run the same version with the same limits.
//
// Identity: functions are user-scoped. Storage keeps names globally unique
// (v1 simplification) with an owner_id column. Routing supports:
//   - /f/<name>/...            legacy + own-namespace shorthand
//   - /f/<owner>/<name>/...    explicit namespace
//   - /s/<slug>/...            random public slug (sharing without namespace)
package functions

import (
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"actions/internal/auth"
	"actions/internal/deploy"
	"actions/internal/runner"
	"actions/internal/store"
)

// Info carries auth/identity alongside the runnable ref.
type Info struct {
	Ref      runner.FunctionRef
	OwnerID  string
	AuthMode string
	Slug     string
}

func refFromStore(fn store.Function, sidecarURL string) Info {
	ref := runner.FunctionRef{
		Name:       fn.Name,
		Version:    fn.ActiveVersion,
		Image:      "actions/" + strings.ToLower(fn.Name) + ":" + fn.ActiveVersion,
		Timeout:    10 * time.Second,
		MemoryMB:   256,
		SidecarURL: sidecarURL,
	}
	if cfg, err := deploy.ParseConfig(fn.ConfigTOML); err == nil {
		if cfg.TimeoutMs != 0 {
			ref.Timeout = time.Duration(cfg.TimeoutMs) * time.Millisecond
		}
		if cfg.MemoryMB != 0 {
			ref.MemoryMB = cfg.MemoryMB
		}
		ref.AllowEgress = cfg.AllowEgress
	}
	mode := fn.AuthMode
	if mode == "" {
		mode = auth.ModePublic
	}
	return Info{Ref: ref, OwnerID: fn.OwnerID, AuthMode: mode, Slug: fn.Slug}
}

func Resolve(st *store.Store, name, devImageFallback, sidecarURL string) (runner.FunctionRef, bool) {
	info, ok := ResolveFull(st, name, sidecarURL)
	if ok {
		return info.Ref, true
	}
	if devImageFallback != "" && name == "hello" {
		return runner.FunctionRef{
			Name: "hello", Version: "dev", Image: devImageFallback,
			Timeout: 10 * time.Second, MemoryMB: 256, SidecarURL: sidecarURL,
		}, true
	}
	return runner.FunctionRef{}, false
}

// ResolveFull is the owner-agnostic global lookup (scheduler + legacy).
func ResolveFull(st *store.Store, name, sidecarURL string) (Info, bool) {
	if fn, err := st.GetFunction(name); err == nil && fn.ActiveVersion != "" {
		return refFromStore(fn, sidecarURL), true
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("registry lookup failed", "fn", name, "err", err)
	}
	return Info{}, false
}

// ResolveNamespaced resolves owner/name.
func ResolveNamespaced(st *store.Store, owner, name, sidecarURL string) (Info, bool) {
	if fn, err := st.GetFunctionNamespaced(owner, name); err == nil && fn.ActiveVersion != "" {
		return refFromStore(fn, sidecarURL), true
	}
	return Info{}, false
}

// ResolveBySlug resolves /s/<slug>.
func ResolveBySlug(st *store.Store, slug, sidecarURL string) (Info, bool) {
	if fn, err := st.GetFunctionBySlug(slug); err == nil && fn.ActiveVersion != "" {
		return refFromStore(fn, sidecarURL), true
	}
	return Info{}, false
}

func SidecarURLFromEnv(port string) string {
	if v := os.Getenv("ACTIONS_SIDECAR_URL"); v != "" {
		return v
	}
	// Dev default: runner containers reach the host control plane here.
	// Compose overrides with http://controlplane:8080.
	return "http://host.docker.internal:" + port
}
