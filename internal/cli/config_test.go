package cli

import (
	"os"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.com":    "https://example.com",
		"https://example.com/":   "https://example.com",
		"http://localhost:8080":  "http://localhost:8080",
		"http://localhost:8080/": "http://localhost:8080",
		"example.com":            "https://example.com",
		"example.com/":           "https://example.com",
		"  example.com:8443  ":   "https://example.com:8443",
		"HTTP://EXAMPLE.COM/":    "HTTP://EXAMPLE.COM",
		"HTTPS://EXAMPLE.COM/a/": "HTTPS://EXAMPLE.COM/a",
	} {
		if got := NormalizeURL(in); got != want {
			t.Fatalf("NormalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeURL(""); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestResolveURLPrefersHTTPSForBareHosts(t *testing.T) {
	t.Setenv("ACTIONS_AUTH_FILE", t.TempDir()+"/auth.json") // isolate saved login
	t.Setenv("ACTIONS_URL", "panel.example.com")
	if got := ResolveURL(""); got != "https://panel.example.com" {
		t.Fatalf("got %q", got)
	}
	if got := ResolveURL("http://localhost:8080"); got != "http://localhost:8080" {
		t.Fatalf("explicit http must be kept, got %q", got)
	}
	os.Unsetenv("ACTIONS_URL")
	if got := ResolveURL(""); got != "http://localhost:8080" {
		t.Fatalf("default got %q", got)
	}
}
