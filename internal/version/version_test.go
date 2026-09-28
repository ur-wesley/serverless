package version

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVersionMatchesPackageJSON(t *testing.T) {
	if Version == "" || Version == "dev" {
		t.Fatalf("Version must come from package.json, got %q", Version)
	}
	_, thisFile, _, _ := runtime.Caller(0)
	for _, p := range []string{
		filepath.Join(filepath.Dir(thisFile), "package.json"),             // embedded copy
		filepath.Join(filepath.Dir(thisFile), "..", "..", "package.json"), // source of truth
	} {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if Version != m.Version {
			t.Fatalf("Version %q != %s %q", Version, p, m.Version)
		}
	}
}
