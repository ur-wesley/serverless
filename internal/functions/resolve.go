// Package functions resolves a function name to a runnable FunctionRef
// from the SQLite registry. Shared by the gateway router and the scheduler
// so HTTP, cron and queue triggers run the same version with the same limits.
package functions

import (
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"time"

	"actions/internal/deploy"
	"actions/internal/runner"
	"actions/internal/store"
)

func Resolve(st *store.Store, name, devImageFallback, sidecarURL string) (runner.FunctionRef, bool) {
	if fn, err := st.GetFunction(name); err == nil && fn.ActiveVersion != "" {
		ref := runner.FunctionRef{
			Name:       name,
			Version:    fn.ActiveVersion,
			Image:      "actions/" + name + ":" + fn.ActiveVersion,
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
		return ref, true
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("registry lookup failed", "fn", name, "err", err)
	}
	if devImageFallback != "" && name == "hello" {
		return runner.FunctionRef{
			Name: "hello", Version: "dev", Image: devImageFallback,
			Timeout: 10 * time.Second, MemoryMB: 256, SidecarURL: sidecarURL,
		}, true
	}
	return runner.FunctionRef{}, false
}

func SidecarURLFromEnv(port string) string {
	if v := os.Getenv("ACTIONS_SIDECAR_URL"); v != "" {
		return v
	}
	// Dev default: runner containers reach the host control plane here.
	// Compose overrides with http://controlplane:8080.
	return "http://host.docker.internal:" + port
}
