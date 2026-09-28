// Package version exposes the platform version.
// The version lives in the repository-root package.json. Because go:embed
// forbids ".." patterns, internal/version/package.json is a synced copy of
// the root file (same "version" field); version_test.go fails if they drift.
// To sync: copy the root package.json over internal/version/package.json.
package version

import (
	_ "embed"
	"encoding/json"
)

//go:generate cp ../../package.json package.json

//go:embed package.json
var packageJSON []byte

// Version is the platform version from the root package.json.
// Falls back to "dev" when package.json is missing or unparsable
// (e.g. stripped builds).
var Version = func() string {
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(packageJSON, &m); err != nil {
		return "dev"
	}
	if m.Version == "" {
		return "dev"
	}
	return m.Version
}()
