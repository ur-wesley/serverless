// Package store is the SQLite WAL metadata registry (functions + versions,
// users + sessions + device codes + API keys).
// Queries mirror db/queries.sql (sqlc source of truth); this hand-written
// layer stands in until sqlc codegen runs in CI. schema.sql here must stay
// identical to db/schema.sql (enforced by TestSchemaInSync).
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

//go:embed migrations/*.sql
var embedMigrations embed.FS

func init() {
	goose.SetBaseFS(embedMigrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		panic(err)
	}
	goose.SetLogger(goose.NopLogger())
}

type Function struct {
	Name          string
	ActiveVersion string
	ConfigTOML    string
	OwnerID       string
	Slug          string
	AuthMode      string
}

type Version struct {
	Name       string
	Ver        string
	SHA256     string
	HandlerKey string
	ConfigJSON string
	Status     string
	OwnerID    string
}

type User struct {
	ID        string
	Name      string
	CreatedAt string
}

type Session struct {
	UserID    string
	CreatedAt string
	ExpiresAt string
}

type Device struct {
	CodeHash   string
	UserCode   string
	UserID     string
	Status     string // pending|approved|denied
	TokenHash  string
	DeviceName string
	ExpiresAt  string
}

type APIKey struct {
	ID         string
	OwnerID    string
	FnName     string
	Name       string
	Prefix     string
	CreatedAt  string
	RevokedAt  string
	LastUsedAt string
}

type Store struct {
	db *sql.DB
}

func newID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newSlug mints an 8-char public slug (same alphabet as auth.NewSlug).
func newSlug() (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	var b [8]byte
	for i, v := range raw {
		b[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(b[:]), nil
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
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// migrate brings the database to the current version with goose
// (internal/store/migrations/*.sql, embedded). Fresh databases apply all
// migrations; legacy pre-goose databases are baselined first (see below).
func (s *Store) migrate() error {
	if err := s.baselineLegacy(); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := goose.Up(s.db, "migrations"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	s.backfillSlugs()
	return nil
}

// baselineLegacy handles databases created before goose was introduced
// (no goose_db_version table):
//   - true legacy (old tables, no slug column): nothing to stamp; goose.Up
//     runs 00001 as a no-op (IF NOT EXISTS) and applies 00002.
//   - already migrated by the previous hand-rolled code (slug column
//     present): stamp version 2 so goose does not re-run the ALTERs.
func (s *Store) baselineLegacy() error {
	var name string
	if err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'functions'`,
	).Scan(&name); err != nil {
		return nil // fresh database; goose.Up applies everything
	}
	var verTable string
	if err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&verTable); err == nil {
		return nil // already goose-managed
	}
	if _, err := goose.EnsureDBVersionContext(context.Background(), s.db); err != nil {
		return err
	}
	if !hasColumn(s.db, "functions", "slug") {
		return nil // true legacy; let goose.Up apply 00001 (no-op) + 00002
	}
	// Mark 00001+00002 applied: 00001 is a no-op on existing tables and the
	// ALTERs/indexes of 00002 are already in place. Both rows are needed —
	// goose refuses gaps before the current version.
	for _, v := range []int64{1, 2} {
		if _, err := s.db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, v); err != nil {
			return err
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) bool {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			continue
		}
		if name == column {
			return true
		}
	}
	return false
}

// backfillSlugs assigns random slugs to legacy functions that predate the
// slug column (stored as ”), so /s/<slug> works for them too.
func (s *Store) backfillSlugs() {
	rows, err := s.db.Query(`SELECT name FROM functions WHERE slug = '' OR slug IS NULL`)
	if err != nil {
		return
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			names = append(names, n)
		}
	}
	rows.Close()
	for _, n := range names {
		for i := 0; i < 5; i++ {
			slug, err := newSlug()
			if err != nil {
				break
			}
			if _, err := s.db.Exec(`UPDATE functions SET slug = ? WHERE name = ? AND (slug = '' OR slug IS NULL)`, slug, n); err != nil {
				break
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM functions WHERE slug = ?`, slug).Scan(&count); err == nil && count == 1 {
				break
			}
		}
	}
}

func (s *Store) Close() error { return s.db.Close() }

func scanFunction(row *sql.Row) (Function, error) {
	var f Function
	err := row.Scan(&f.Name, &f.ActiveVersion, &f.ConfigTOML, &f.OwnerID, &f.Slug, &f.AuthMode)
	if err != nil {
		return Function{}, err
	}
	if f.AuthMode == "" {
		f.AuthMode = "public"
	}
	return f, nil
}

func (s *Store) GetFunction(name string) (Function, error) {
	return scanFunction(s.db.QueryRow(
		`SELECT name, active_version, config_toml, owner_id, slug, auth_mode FROM functions WHERE name = ?`, name,
	))
}

func (s *Store) GetFunctionOwned(ownerID, name string) (Function, error) {
	return scanFunction(s.db.QueryRow(
		`SELECT name, active_version, config_toml, owner_id, slug, auth_mode FROM functions WHERE name = ? AND (owner_id = ? OR owner_id = '')`, name, ownerID,
	))
}

func (s *Store) GetFunctionBySlug(slug string) (Function, error) {
	if slug == "" {
		return Function{}, sql.ErrNoRows
	}
	return scanFunction(s.db.QueryRow(
		`SELECT name, active_version, config_toml, owner_id, slug, auth_mode FROM functions WHERE slug = ?`, slug,
	))
}

// GetFunctionNamespaced resolves ownerName/name (URL namespace /f/<user>/<name>).
func (s *Store) GetFunctionNamespaced(ownerName, name string) (Function, error) {
	u, err := s.GetUserByName(ownerName)
	if err != nil {
		return Function{}, err
	}
	row := s.db.QueryRow(
		`SELECT name, active_version, config_toml, owner_id, slug, auth_mode FROM functions WHERE name = ? AND owner_id = ?`, name, u.ID,
	)
	return scanFunction(row)
}

func (s *Store) ListFunctions() ([]Function, error) {
	return s.listFunctions(`SELECT name, active_version, config_toml, owner_id, slug, auth_mode FROM functions ORDER BY name`)
}

func (s *Store) ListFunctionsByOwner(ownerID string) ([]Function, error) {
	if ownerID == "" {
		return s.ListFunctions()
	}
	return s.listFunctions(
		`SELECT name, active_version, config_toml, owner_id, slug, auth_mode FROM functions WHERE owner_id = ? OR owner_id = '' ORDER BY name`, ownerID)
}

func (s *Store) listFunctions(query string, args ...any) ([]Function, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Function
	for rows.Next() {
		var f Function
		if err := rows.Scan(&f.Name, &f.ActiveVersion, &f.ConfigTOML, &f.OwnerID, &f.Slug, &f.AuthMode); err != nil {
			return nil, err
		}
		if f.AuthMode == "" {
			f.AuthMode = "public"
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpsertFunction keeps the legacy signature (unowned / bootstrap).
func (s *Store) UpsertFunction(name, activeVersion, configTOML string) error {
	return s.UpsertFunctionOwned("", name, activeVersion, configTOML, "", "public")
}

// UpsertFunctionOwned inserts or updates a function, preserving an existing
// slug when slug == "".
func (s *Store) UpsertFunctionOwned(ownerID, name, activeVersion, configTOML, slug, authMode string) error {
	if authMode == "" {
		authMode = "public"
	}
	if slug != "" {
		_, err := s.db.Exec(
			`INSERT INTO functions (name, active_version, config_toml, owner_id, slug, auth_mode) VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT (name) DO UPDATE SET active_version = excluded.active_version, config_toml = excluded.config_toml,
			 owner_id = excluded.owner_id, slug = excluded.slug, auth_mode = excluded.auth_mode`,
			name, activeVersion, configTOML, ownerID, slug, authMode,
		)
		return err
	}
	// Preserve existing slug/owner when not supplied (legacy deploys).
	existing, err := s.GetFunction(name)
	if err == nil {
		if ownerID == "" {
			ownerID = existing.OwnerID
		}
		slug = existing.Slug
		if authMode == "public" && existing.AuthMode != "" {
			authMode = existing.AuthMode
		}
	}
	_, err = s.db.Exec(
		`INSERT INTO functions (name, active_version, config_toml, owner_id, slug, auth_mode) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (name) DO UPDATE SET active_version = excluded.active_version, config_toml = excluded.config_toml,
		 owner_id = excluded.owner_id, slug = excluded.slug, auth_mode = excluded.auth_mode`,
		name, activeVersion, configTOML, ownerID, slug, authMode,
	)
	return err
}

func (s *Store) SetFunctionSlug(name, slug string) error {
	_, err := s.db.Exec(`UPDATE functions SET slug = ? WHERE name = ?`, slug, name)
	return err
}

func (s *Store) SetFunctionAuthMode(name, mode string) error {
	_, err := s.db.Exec(`UPDATE functions SET auth_mode = ? WHERE name = ?`, mode, name)
	return err
}

func (s *Store) SlugExists(slug string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM functions WHERE slug = ?`, slug).Scan(&n)
	return n > 0
}

func (s *Store) InsertVersion(v Version) error {
	_, err := s.db.Exec(
		`INSERT INTO versions (name, ver, sha256, handler_key, config_json, status, owner_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		v.Name, v.Ver, v.SHA256, v.HandlerKey, v.ConfigJSON, v.Status, v.OwnerID,
	)
	return err
}

func (s *Store) GetVersion(name, ver string) (Version, error) {
	var v Version
	err := s.db.QueryRow(
		`SELECT name, ver, sha256, handler_key, config_json, status, owner_id FROM versions WHERE name = ? AND ver = ?`,
		name, ver,
	).Scan(&v.Name, &v.Ver, &v.SHA256, &v.HandlerKey, &v.ConfigJSON, &v.Status, &v.OwnerID)
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

// --- users ---

func (s *Store) CountUsers() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n
}

func (s *Store) CreateUser(name, passwordHash string) (User, error) {
	id := newID(16)
	_, err := s.db.Exec(`INSERT INTO users (id, name, password_hash) VALUES (?, ?, ?)`, id, strings.ToLower(name), passwordHash)
	if err != nil {
		return User{}, err
	}
	return s.GetUserByID(id)
}

func (s *Store) GetUserByName(name string) (User, error) {
	var u User
	err := s.db.QueryRow(`SELECT id, name, created_at FROM users WHERE name = ?`, strings.ToLower(name)).Scan(&u.ID, &u.Name, &u.CreatedAt)
	return u, err
}

func (s *Store) GetUserByID(id string) (User, error) {
	var u User
	err := s.db.QueryRow(`SELECT id, name, created_at FROM users WHERE id = ?`, id).Scan(&u.ID, &u.Name, &u.CreatedAt)
	return u, err
}

func (s *Store) getPasswordHash(userID string) (string, error) {
	var h string
	err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&h)
	return h, err
}

// CheckUserPassword verifies a username+password pair.
func (s *Store) CheckUserPassword(name, password string, check func(hash, pw string) bool) (User, error) {
	u, err := s.GetUserByName(name)
	if err != nil {
		return User{}, err
	}
	h, err := s.getPasswordHash(u.ID)
	if err != nil {
		return User{}, err
	}
	if !check(h, password) {
		return User{}, fmt.Errorf("invalid credentials")
	}
	return u, nil
}

// --- sessions ---

func (s *Store) CreateSession(tokenHash, userID string, expiresAt time.Time) error {
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		tokenHash, userID, expiresAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) GetSessionUser(tokenHash string) (User, bool) {
	var userID, expiresAt string
	if err := s.db.QueryRow(`SELECT user_id, expires_at FROM sessions WHERE token_hash = ?`, tokenHash).Scan(&userID, &expiresAt); err != nil {
		return User{}, false
	}
	if t, err := time.Parse(time.RFC3339, expiresAt); err == nil && time.Now().After(t) {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
		return User{}, false
	}
	u, err := s.GetUserByID(userID)
	if err != nil {
		return User{}, false
	}
	return u, true
}

func (s *Store) DeleteSession(tokenHash string) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
}

// --- device codes ---

func (s *Store) CreateDevice(codeHash, userCode, deviceName string, expiresAt time.Time) error {
	_, err := s.db.Exec(`INSERT INTO device_codes (code_hash, user_code, device_name, expires_at) VALUES (?, ?, ?, ?)`,
		codeHash, userCode, deviceName, expiresAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) GetDeviceByHash(codeHash string) (Device, error) {
	var d Device
	err := s.db.QueryRow(`SELECT code_hash, user_code, user_id, status, token_hash, device_name, expires_at FROM device_codes WHERE code_hash = ?`,
		codeHash).Scan(&d.CodeHash, &d.UserCode, &d.UserID, &d.Status, &d.TokenHash, &d.DeviceName, &d.ExpiresAt)
	return d, err
}

func (s *Store) GetDeviceByUserCode(userCode string) (Device, error) {
	var d Device
	err := s.db.QueryRow(`SELECT code_hash, user_code, user_id, status, token_hash, device_name, expires_at FROM device_codes WHERE user_code = ?`,
		userCode).Scan(&d.CodeHash, &d.UserCode, &d.UserID, &d.Status, &d.TokenHash, &d.DeviceName, &d.ExpiresAt)
	return d, err
}

func (s *Store) ApproveDevice(userCode, userID, tokenHash string) error {
	_, err := s.db.Exec(`UPDATE device_codes SET user_id = ?, status = 'approved', token_hash = ? WHERE user_code = ?`,
		userID, tokenHash, userCode)
	return err
}

func (s *Store) DenyDevice(userCode string) error {
	_, err := s.db.Exec(`UPDATE device_codes SET status = 'denied' WHERE user_code = ?`, userCode)
	return err
}

func (s *Store) DeleteDevice(codeHash string) {
	_, _ = s.db.Exec(`DELETE FROM device_codes WHERE code_hash = ?`, codeHash)
}

// --- api keys ---

func (s *Store) CreateAPIKey(ownerID, fnName, name, prefix, keyHash string) (APIKey, error) {
	id := newID(8)
	_, err := s.db.Exec(`INSERT INTO api_keys (id, owner_id, fn_name, name, prefix, key_hash) VALUES (?, ?, ?, ?, ?, ?)`,
		id, ownerID, fnName, name, prefix, keyHash)
	if err != nil {
		return APIKey{}, err
	}
	return s.getAPIKey(id)
}

func (s *Store) getAPIKey(id string) (APIKey, error) {
	var k APIKey
	err := s.db.QueryRow(`SELECT id, owner_id, fn_name, name, prefix, created_at, revoked_at, last_used_at FROM api_keys WHERE id = ?`,
		id).Scan(&k.ID, &k.OwnerID, &k.FnName, &k.Name, &k.Prefix, &k.CreatedAt, &k.RevokedAt, &k.LastUsedAt)
	return k, err
}

func (s *Store) ListAPIKeys(ownerID, fnName string) ([]APIKey, error) {
	rows, err := s.db.Query(`SELECT id, owner_id, fn_name, name, prefix, created_at, revoked_at, last_used_at FROM api_keys WHERE owner_id = ? AND fn_name = ? ORDER BY created_at`,
		ownerID, fnName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.OwnerID, &k.FnName, &k.Name, &k.Prefix, &k.CreatedAt, &k.RevokedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// FindAPIKey locates a non-revoked key for fn by its sha256 hash.
func (s *Store) FindAPIKey(fnName, keyHash string) (APIKey, bool) {
	var k APIKey
	err := s.db.QueryRow(`SELECT id, owner_id, fn_name, name, prefix, created_at, revoked_at, last_used_at FROM api_keys
		WHERE fn_name = ? AND key_hash = ? AND revoked_at = ''`, fnName, keyHash).Scan(
		&k.ID, &k.OwnerID, &k.FnName, &k.Name, &k.Prefix, &k.CreatedAt, &k.RevokedAt, &k.LastUsedAt)
	if err != nil {
		return APIKey{}, false
	}
	return k, true
}

func (s *Store) RevokeAPIKey(ownerID, id string) error {
	res, err := s.db.Exec(`UPDATE api_keys SET revoked_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ? AND owner_id = ? AND revoked_at = ''`, id, ownerID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("key not found")
	}
	return nil
}

func (s *Store) TouchAPIKey(id string) {
	_, _ = s.db.Exec(`UPDATE api_keys SET last_used_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`, id)
}
