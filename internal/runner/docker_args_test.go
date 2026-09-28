package runner

import (
	"strings"
	"testing"
)

func testRef() FunctionRef {
	return FunctionRef{Name: "Echo", Version: "v1", Image: "actions/echo:v1", MemoryMB: 256}
}

func hasFlag(args []string, flag, val string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == val {
			return true
		}
	}
	return false
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestBuildRunArgsShared(t *testing.T) {
	args := buildRunArgs(testRef(), "actions-echo-abc123", "12345", "actions-net", 256, "http://x", "tok", false)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--read-only", "--cap-drop", "--pids-limit", "actions-managed=true",
		"actions-fn=echo", "--memory-swap", "256m", "ACTIONS_SIDECAR_TOKEN=tok",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	if !hasFlag(args, "-p", "127.0.0.1:12345:8080") {
		t.Fatalf("missing port publish in %s", joined)
	}
	if !hasFlag(args, "--network", "actions-net") {
		t.Fatalf("missing network in %s", joined)
	}
	if hasArg(args, "--runtime=runsc") {
		t.Fatalf("runsc flag without opt-in: %s", joined)
	}
}

func TestBuildRunArgsIsolated(t *testing.T) {
	args := buildRunArgs(testRef(), "actions-echo-abc123", "", "actions-sandbox", 256, "http://x", "", true)
	joined := strings.Join(args, " ")
	if hasFlag(args, "-p", "127.0.0.1:12345:8080") || strings.Contains(joined, " -p ") {
		t.Fatalf("isolated must not publish ports: %s", joined)
	}
	if !hasFlag(args, "--network", "actions-sandbox") {
		t.Fatalf("missing sandbox net: %s", joined)
	}
	if !hasArg(args, "--runtime=runsc") {
		t.Fatalf("missing runsc flag: %s", joined)
	}
	if strings.Contains(joined, "ACTIONS_SIDECAR_TOKEN") {
		t.Fatalf("empty token must be omitted: %s", joined)
	}
}

func TestSanitize(t *testing.T) {
	if sanitize("Echo_1!") != "echo-1-" {
		t.Fatalf("got %q", sanitize("Echo_1!"))
	}
}
