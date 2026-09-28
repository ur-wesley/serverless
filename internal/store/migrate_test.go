package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestOpenMigratesLegacyDB reproduces the prod crash
// ("migrate: SQL logic error: no such column: slug"): a database created by
// an older release (no owner_id/slug/auth_mode columns) must open cleanly,
// keep its data, and get slugs backfilled.
func TestOpenMigratesLegacyDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE functions (
		   name TEXT PRIMARY KEY,
		   active_version TEXT NOT NULL DEFAULT '',
		   config_toml TEXT NOT NULL DEFAULT ''
		 )`,
		`CREATE TABLE versions (
		   name TEXT NOT NULL,
		   ver TEXT NOT NULL,
		   sha256 TEXT NOT NULL,
		   handler_key TEXT NOT NULL,
		   config_json TEXT NOT NULL DEFAULT '{}',
		   status TEXT NOT NULL DEFAULT 'queued',
		   created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		   PRIMARY KEY (name, ver)
		 )`,
		`INSERT INTO functions (name, active_version, config_toml) VALUES ('hello', 'v1', 'name="hello"')`,
		`INSERT INTO versions (name, ver, sha256, handler_key, status) VALUES ('hello', 'v1', 'abc', 'bundles/hello/v1/handler', 'active')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy DB: %v", err)
	}
	defer s.Close()

	f, err := s.GetFunction("hello")
	if err != nil {
		t.Fatal(err)
	}
	if f.ActiveVersion != "v1" {
		t.Fatalf("active = %q, want v1", f.ActiveVersion)
	}
	if f.AuthMode != "public" {
		t.Fatalf("auth_mode = %q, want public default", f.AuthMode)
	}
	if len(f.Slug) != 8 {
		t.Fatalf("slug = %q, want backfilled 8-char slug", f.Slug)
	}
	if _, err := s.GetFunctionBySlug(f.Slug); err != nil {
		t.Fatalf("bySlug: %v", err)
	}
	if _, err := s.GetVersion("hello", "v1"); err != nil {
		t.Fatalf("version lost: %v", err)
	}
	// Second open must be a clean no-op (idempotent migration).
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	f2, _ := s2.GetFunction("hello")
	if f2.Slug != f.Slug {
		t.Fatal("slug changed on reopen")
	}
	var ver int64
	if err := s2.db.QueryRow(`SELECT max(version_id) FROM goose_db_version`).Scan(&ver); err != nil || ver != 2 {
		t.Fatalf("goose version = %d, err = %v; want 2", ver, err)
	}
}

// TestOpenHandMigratedDB covers databases migrated by the previous
// hand-rolled code (new columns present, no goose_db_version table):
// they must open cleanly and get stamped at version 2 instead of
// re-running the ALTERs (which would fail with duplicate column).
func TestOpenHandMigratedDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hand.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE functions (
		   name TEXT PRIMARY KEY,
		   active_version TEXT NOT NULL DEFAULT '',
		   config_toml TEXT NOT NULL DEFAULT '',
		   owner_id TEXT NOT NULL DEFAULT '',
		   slug TEXT NOT NULL DEFAULT '',
		   auth_mode TEXT NOT NULL DEFAULT 'public'
		 )`,
		`CREATE TABLE versions (
		   name TEXT NOT NULL,
		   ver TEXT NOT NULL,
		   sha256 TEXT NOT NULL,
		   handler_key TEXT NOT NULL,
		   config_json TEXT NOT NULL DEFAULT '{}',
		   status TEXT NOT NULL DEFAULT 'queued',
		   created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		   owner_id TEXT NOT NULL DEFAULT '',
		   PRIMARY KEY (name, ver)
		 )`,
		`INSERT INTO functions (name, active_version, config_toml, slug) VALUES ('hello', 'v1', 'name="hello"', 'keepme01')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open hand-migrated DB: %v", err)
	}
	defer s.Close()
	f, err := s.GetFunction("hello")
	if err != nil {
		t.Fatal(err)
	}
	if f.Slug != "keepme01" {
		t.Fatalf("slug = %q, want preserved keepme01", f.Slug)
	}
	var ver int64
	if err := s.db.QueryRow(`SELECT max(version_id) FROM goose_db_version`).Scan(&ver); err != nil || ver != 2 {
		t.Fatalf("goose version = %d, err = %v; want 2", ver, err)
	}
}
