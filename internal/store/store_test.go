package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSchemaInSync(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "db", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != schemaSQL {
		t.Fatal("internal/store/schema.sql drifted from db/schema.sql; copy it over")
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertFunction("hello", "", "name=\"hello\""); err != nil {
		t.Fatal(err)
	}
	ver, err := s.NextVersion("hello")
	if err != nil {
		t.Fatal(err)
	}
	if ver != "v1" {
		t.Fatalf("ver = %q, want v1", ver)
	}
	if err := s.InsertVersion(Version{
		Name: "hello", Ver: ver, SHA256: "abc",
		HandlerKey: "bundles/hello/v1/handler", ConfigJSON: "{}", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertFunction("hello", ver, "name=\"hello\""); err != nil {
		t.Fatal(err)
	}
	f, err := s.GetFunction("hello")
	if err != nil {
		t.Fatal(err)
	}
	if f.ActiveVersion != "v1" {
		t.Fatalf("active = %q, want v1", f.ActiveVersion)
	}
	v2, err := s.NextVersion("hello")
	if err != nil {
		t.Fatal(err)
	}
	if v2 != "v2" {
		t.Fatalf("next = %q, want v2", v2)
	}
	fns, err := s.ListFunctions()
	if err != nil || len(fns) != 1 {
		t.Fatalf("list = %+v, err = %v", fns, err)
	}
}
