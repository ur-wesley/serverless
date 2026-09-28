package cli

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ur-wesley/serverless/internal/deploy"
)

func TestInitGo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myfn")
	if err := Init("go", "myfn", dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"actions.toml", "go.mod", "main.go"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "actions.toml"))
	cfg, err := deploy.ParseConfig(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := deploy.Validate("myfn", cfg); err != nil {
		t.Fatal(err)
	}
	// Refusing to overwrite.
	if err := Init("go", "myfn", dir); err == nil {
		t.Fatal("expected overwrite error")
	}
}

func TestInitTS(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myts")
	if err := Init("ts", "myts", dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"actions.toml", "package.json", "src/index.ts"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
}

func TestInitBadRuntime(t *testing.T) {
	if err := Init("py", "x", t.TempDir()); err == nil {
		t.Fatal("expected error")
	}
}

func TestFormatFunctions(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	fns := []Function{
		{
			Name: "hello-go", ActiveVersion: "v1",
			ConfigTOML: "name=\"hello-go\"\nruntime=\"go\"\nroute=\"/f/hello-go\"\ntimeout_ms=10000\nmemory_mb=256\nallow_egress=false\nqueue=[\"orders.created\"]\n",
			DeployedAt: "2026-09-28T11:50:00Z",
			Warm: []WarmInstance{{
				Name: "hello-go", Version: "v1", Container: "abc123",
				StartedAt: now.Add(-3*time.Minute - 12*time.Second),
				LastUsed:  now.Add(-5 * time.Second),
			}},
		},
		{Name: "echo", ActiveVersion: "v2", ConfigTOML: "name=\"echo\"\nruntime=\"go\"\n"},
		{Name: "broken", ActiveVersion: "v1", ConfigTOML: "not toml [[["},
	}
	out := FormatFunctions(fns, now)
	for _, want := range []string{
		"NAME", "STATUS", "UPTIME", "LAST USED", "DEPLOYED", "TRIGGERS",
		"hello-go", "v1", "go", "/f/hello-go", "warm", "3m12s", "5s ago",
		"09-28 11:50", "256", "10s", "no", "q:orders.created",
		"echo", "cold", "-", "/f/echo",
		"broken", "/f/broken",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestZipDirSkips(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "actions.toml"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "node_modules", "big.js"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "a.ts"), []byte("x"), 0o644)
	z, err := ZipDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["actions.toml"] || !names["src/a.ts"] || names["node_modules/big.js"] {
		t.Fatalf("names = %v", names)
	}
}
