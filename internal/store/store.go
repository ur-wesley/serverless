// Package store is the SQLite WAL metadata registry (functions + versions).
// Queries mirror db/queries.sql (sqlc source of truth); this hand-written
// layer stands in until sqlc codegen runs in CI. schema.sql here must stay
// identical to db/schema.sql (enforced by TestSchemaInSync).
package store

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type Function struct {
	Name          string
	ActiveVersion string
	ConfigTOML    string
}

type Version struct {
	Name       string
	Ver        string
	SHA256     string
	HandlerKey string
	ConfigJSON string
	Status     string
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if path == "" {
		path = "actions.db"
	}
	db, err := sql.Open("sqlite", path+"?cache=shared")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("wal: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) GetFunction(name string) (Function, error) {
	var f Function
	err := s.db.QueryRow(
		`SELECT name, active_version, config_toml FROM functions WHERE name = ?`, name,
	).Scan(&f.Name, &f.ActiveVersion, &f.ConfigTOML)
	if err != nil {
		return Function{}, err
	}
	return f, nil
}

func (s *Store) ListFunctions() ([]Function, error) {
	rows, err := s.db.Query(`SELECT name, active_version, config_toml FROM functions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Function
	for rows.Next() {
		var f Function
		if err := rows.Scan(&f.Name, &f.ActiveVersion, &f.ConfigTOML); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) UpsertFunction(name, activeVersion, configTOML string) error {
	_, err := s.db.Exec(
		`INSERT INTO functions (name, active_version, config_toml) VALUES (?, ?, ?)
		 ON CONFLICT (name) DO UPDATE SET active_version = excluded.active_version, config_toml = excluded.config_toml`,
		name, activeVersion, configTOML,
	)
	return err
}

func (s *Store) InsertVersion(v Version) error {
	_, err := s.db.Exec(
		`INSERT INTO versions (name, ver, sha256, handler_key, config_json, status) VALUES (?, ?, ?, ?, ?, ?)`,
		v.Name, v.Ver, v.SHA256, v.HandlerKey, v.ConfigJSON, v.Status,
	)
	return err
}

func (s *Store) GetVersion(name, ver string) (Version, error) {
	var v Version
	err := s.db.QueryRow(
		`SELECT name, ver, sha256, handler_key, config_json, status FROM versions WHERE name = ? AND ver = ?`,
		name, ver,
	).Scan(&v.Name, &v.Ver, &v.SHA256, &v.HandlerKey, &v.ConfigJSON, &v.Status)
	if err != nil {
		return Version{}, err
	}
	return v, nil
}

// ActiveVersionCreatedAt returns versions.created_at (UTC, RFC3339-ish) for a
// deployed version, or "" when unknown. Used for `actions ls` "deployed" column.
func (s *Store) ActiveVersionCreatedAt(name, ver string) string {
	var created string
	if err := s.db.QueryRow(
		`SELECT created_at FROM versions WHERE name = ? AND ver = ?`, name, ver,
	).Scan(&created); err != nil {
		return ""
	}
	return created
}

// NextVersion returns v<N> where N = existing count + 1.
func (s *Store) NextVersion(name string) (string, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM versions WHERE name = ?`, name).Scan(&n)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("v%d", n+1), nil
}
