package deploy

import (
	"testing"

	"actions/internal/store"
)

func TestParseVersion(t *testing.T) {
	cases := map[string]string{
		"1.2.3":      "1.2.3",
		"v1.2.3":     "1.2.3",
		"1.2":        "1.2.0",
		"v3":         "3.0.0", // legacy counter
		"0.1.0":      "0.1.0",
		"2.0.0-beta": "2.0.0-beta",
		"1.2.3+b1":   "1.2.3",
	}
	for in, want := range cases {
		v, err := ParseVersion(in)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", in, err)
		}
		if v.Raw != want {
			t.Fatalf("ParseVersion(%q) = %q, want %q", in, v.Raw, want)
		}
	}
	for _, bad := range []string{"", "x", "1", "1.2.3.4", "a.b.c"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Fatalf("ParseVersion(%q) should fail", bad)
		}
	}
}

func TestCompare(t *testing.T) {
	lt := [][2]string{
		{"1.0.0", "2.0.0"},
		{"1.2.3", "1.10.0"},
		{"1.0.0-alpha", "1.0.0"},
		{"1.0.0-alpha", "1.0.0-alpha.1"},
		{"1.0.0-alpha.1", "1.0.0-beta"},
		{"v1", "1.0.1"},
	}
	for _, c := range lt {
		a, _ := ParseVersion(c[0])
		b, _ := ParseVersion(c[1])
		if Compare(a, b) >= 0 {
			t.Fatalf("Compare(%q,%q) should be <0", c[0], c[1])
		}
		if Compare(b, a) <= 0 {
			t.Fatalf("Compare(%q,%q) should be >0", c[1], c[0])
		}
	}
	a, _ := ParseVersion("v1.2.3")
	b, _ := ParseVersion("1.2.3")
	if Compare(a, b) != 0 {
		t.Fatal("v1.2.3 should equal 1.2.3")
	}
}

func TestResolveVersion(t *testing.T) {
	s := testService(t)
	must := func(name, toml string) string {
		t.Helper()
		cfg, err := ParseConfig(toml)
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(name, cfg); err != nil {
			t.Fatal(err)
		}
		ver, err := s.ResolveVersion(name, cfg)
		if err != nil {
			t.Fatal(err)
		}
		return ver
	}
	mustFail := func(name, toml string) {
		t.Helper()
		cfg, err := ParseConfig(toml)
		if err != nil {
			return // parse failure counts as rejection
		}
		if err := Validate(name, cfg); err != nil {
			return
		}
		if _, err := s.ResolveVersion(name, cfg); err == nil {
			t.Fatalf("ResolveVersion(%q) should fail", toml)
		}
	}

	base := "name=\"hello\"\nruntime=\"go\"\n"
	// First deploy defaults to 0.1.0.
	if v := must("hello", base); v != "0.1.0" {
		t.Fatalf("first = %q, want 0.1.0", v)
	}
	// Record 0.1.0 as active, then auto-bump patch.
	if err := s.Store.UpsertFunctionOwned("", "hello", "0.1.0", base, "slug0001", "public"); err != nil {
		t.Fatal(err)
	}
	if v := must("hello", base); v != "0.1.1" {
		t.Fatalf("patch = %q, want 0.1.1", v)
	}
	if v := must("hello", base+"bump=\"minor\"\n"); v != "0.2.0" {
		t.Fatalf("minor = %q, want 0.2.0", v)
	}
	if v := must("hello", base+"bump=\"major\"\n"); v != "1.0.0" {
		t.Fatalf("major = %q, want 1.0.0", v)
	}
	// Explicit version above active wins.
	if v := must("hello", base+"version=\"2.0.0\"\n"); v != "2.0.0" {
		t.Fatalf("explicit = %q, want 2.0.0", v)
	}
	// Strictness: equal/below active rejected; version+bump rejected.
	mustFail("hello", base+"version=\"0.1.0\"\n")
	mustFail("hello", base+"version=\"0.0.9\"\n")
	mustFail("hello", base+"version=\"1.0.0\"\nbump=\"patch\"\n")
	mustFail("hello", base+"bump=\"weekly\"\n")

	// Legacy active v3 -> next auto is 3.0.1; explicit must exceed 3.0.0.
	if err := s.Store.UpsertFunctionOwned("", "legacy", "v3", base, "slug0002", "public"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := ParseConfig(base)
	if v, err := s.ResolveVersion("legacy", cfg); err != nil || v != "3.0.1" {
		t.Fatalf("legacy next = %q, %v; want 3.0.1", v, err)
	}
}

func TestResolveVersionDuplicate(t *testing.T) {
	s := testService(t)
	base := "name=\"dup\"\nruntime=\"go\"\n"
	if err := s.Store.UpsertFunctionOwned("", "dup", "1.0.0", base, "slug0003", "public"); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.InsertVersion(store.Version{
		Name: "dup", Ver: "1.0.1", SHA256: "x", HandlerKey: "k", ConfigJSON: "{}",
		Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	// Auto-bump lands on existing 1.0.1 -> conflict, not silent reuse.
	cfg, _ := ParseConfig(base)
	if _, err := s.ResolveVersion("dup", cfg); err == nil {
		t.Fatal("duplicate auto version should fail")
	}
}
