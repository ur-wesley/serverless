package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultHandlerCmd(t *testing.T) {
	cases := map[string][]string{
		"ts":   {"bun", "src/index.ts"},
		"go":   {"go", "run", "."},
		"rust": {"cargo", "run", "--quiet"},
	}
	for runtime, want := range cases {
		got := defaultHandlerCmd(runtime, ".", nil)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("defaultHandlerCmd(%q) = %q, want %q", runtime, got, want)
		}
	}
	if got := defaultHandlerCmd("go", ".", []string{"custom", "cmd"}); strings.Join(got, " ") != "custom cmd" {
		t.Errorf("explicit cmdArgs not honored: %q", got)
	}
}

func TestSnapshotWatchesRust(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join("src", "main.rs"), "Cargo.toml", "Cargo.lock"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("a"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := snapshotDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join("src", "main.rs"), "Cargo.toml", "Cargo.lock"} {
		if _, ok := snap[name]; !ok {
			t.Errorf("snapshotDir missing %s", name)
		}
	}
}
