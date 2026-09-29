package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestStatusPlainPassthrough(t *testing.T) {
	Plain = true
	defer func() { Plain = false }()
	for _, s := range []string{"active", "failed", "warm", "cold", "weird"} {
		if got := Status(s); got != s {
			t.Fatalf("Status(%q) = %q, want passthrough in plain mode", s, got)
		}
	}
	if got := HTTPStatus(500); got != "500" {
		t.Fatalf("HTTPStatus plain = %q", got)
	}
}

func TestTablePlainContainsHeadersAndCells(t *testing.T) {
	Plain = true
	defer func() { Plain = false }()
	out := Table([]string{"ID", "STATUS"}, [][]string{{"abc123", "active"}}, 1)
	for _, want := range []string{"ID", "STATUS", "abc123", "active"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSpinnerPlainIsNoop(t *testing.T) {
	Plain = true
	defer func() { Plain = false }()
	var b bytes.Buffer
	sp := NewSpinner(&b, "")
	sp.Start()
	sp.Message("working")
	sp.Stop()
	if b.Len() != 0 {
		t.Fatalf("plain spinner wrote %q", b.String())
	}
}

func TestTableFallsBackWithoutTTY(t *testing.T) {
	// No TTY in tests: Enabled() is false even with Plain unset, so Table
	// must degrade to aligned plain text (same contract as pipes).
	Plain = false
	out := Table([]string{"ID", "STATUS"}, [][]string{{"abc123", "active"}}, 1)
	for _, want := range []string{"ID", "STATUS", "abc123", "active"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "╭") {
		t.Fatalf("expected plain fallback without TTY, got borders:\n%s", out)
	}
}
