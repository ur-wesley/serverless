package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ur-wesley/oort/internal/cli/ui"
	"github.com/ur-wesley/oort/internal/version"
)

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ui.Plain = true
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestRootHasAllCommands(t *testing.T) {
	root := NewRootCmd()
	var names []string
	for _, c := range root.Commands() {
		names = append(names, c.Name())
	}
	for _, want := range []string{
		"login", "logout", "whoami", "config", "init", "dev",
		"deploy", "jobs", "invoke", "logs", "ls", "keys", "version",
	} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing command %q (have %v)", want, names)
		}
	}
}

func TestVersionCmd(t *testing.T) {
	out, err := run(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != version.Version {
		t.Fatalf("version = %q, want %q", out, version.Version)
	}
}

func TestVersionFlag(t *testing.T) {
	out, err := run(t, "--version")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != version.Version {
		t.Fatalf("--version = %q, want %q", out, version.Version)
	}
}

func TestUnknownCommandErrors(t *testing.T) {
	if _, err := run(t, "bogus"); err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestInvokeRequiresArg(t *testing.T) {
	if _, err := run(t, "invoke"); err == nil {
		t.Fatal("expected error for missing NAME")
	}
}

func TestInitRequiresRuntimeAndName(t *testing.T) {
	// Non-TTY (ui.Plain): no huh form, straight to usage error.
	if _, err := run(t, "init"); err == nil {
		t.Fatal("expected usage error for empty init")
	}
}

func TestKeysHelpListsSubcommands(t *testing.T) {
	out, err := run(t, "keys", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"create", "ls", "revoke"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in keys help:\n%s", want, out)
		}
	}
}
