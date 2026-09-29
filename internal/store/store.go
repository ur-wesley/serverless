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
//     runs 00001 as a no-op (IF NOT EXISTS) and applies 00002+00003.
//   - already migrated by the previous hand-rolled code (slug column
//     present): ensure auth tables exist (hand-rolled code only added
//     columns), then stamp versions 1-3 so goose does not re-run ALTERs.
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
		return nil // true legacy; let goose.Up apply 00001 (no-op) + 00002 + 00003
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')))`,
		`CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')), expires_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS device_codes (code_hash TEXT PRIMARY KEY, user_code TEXT NOT NULL UNIQUE, user_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending', token_hash TEXT NOT NULL DEFAULT '', device_name TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')), expires_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS api_keys (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fn_name TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', prefix TEXT NOT NULL DEFAULT '', key_hash TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')), revoked_at TEXT NOT NULL DEFAULT '', last_used_at TEXT NOT NULL DEFAULT '')`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_functions_slug ON functions(slug) WHERE slug != ''`,
		`CREATE INDEX IF NOT EXISTS idx_functions_owner ON functions(owner_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_api_keys_fn ON api_keys(owner_id, fn_name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_fn_hash ON api_keys(fn_name, key_hash) WHERE revoked_at = ''`,
		`CREATE TABLE IF NOT EXISTS deploy_jobs (id TEXT PRIMARY KEY, fn_name TEXT NOT NULL, owner_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'queued', config_toml TEXT NOT NULL DEFAULT '', version TEXT NOT NULL DEFAULT '', image TEXT NOT NULL DEFAULT '', sha256 TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', log_key TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')), updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')))`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_fn ON deploy_jobs(fn_name, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_status ON deploy_jobs(status, created_at)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	// Mark 00001-00004 applied. All rows are needed —
	// goose refuses gaps before the current version.
	for _, v := range []int64{1, 2, 3, 4} {
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
// deployed version, or "" when unknown. Used for `oort ls` "deployed" column.
func (s *Store) ActiveVersionCreatedAt(name, ver string) string {
	var created string
	if err := s.db.QueryRow(
		`SELECT created_at FROM versions WHERE name = ? AND ver = ?`, name, ver,
	).Scan(&created); err != nil {
		return ""
	}
	return created
}

// ListVersions returns all versions for a function, oldest first.
func (s *Store) ListVersions(name string) ([]Version, error) {
	rows, err := s.db.Query(
		`SELECT name, ver, sha256, handler_key, config_json, status, owner_id FROM versions WHERE name = ? ORDER BY created_at, ver`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.Name, &v.Ver, &v.SHA256, &v.HandlerKey, &v.ConfigJSON, &v.Status, &v.OwnerID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SetActiveVersion points a function at an existing version.
func (s *Store) SetActiveVersion(name, ver string) error {
	if _, err := s.GetVersion(name, ver); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE functions SET active_version = ? WHERE name = ?`, ver, name)
	return err
}

// DeleteFunction removes a function and its versions.
func (s *Store) DeleteFunction(name string) error {
	if _, err := s.db.Exec(`DELETE FROM versions WHERE name = ?`, name); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM functions WHERE name = ?`, name)
	return err
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

// --- deploy jobs ---

// Job statuses for the async deploy queue.
const (
	JobQueued   = "queued"
	JobBuilding = "building"
	JobActive   = "active"
	JobFailed   = "failed"
)

type Job struct {
	ID         string `json:"id"`
	FnName     string `json:"fn_name"`
	OwnerID    string `json:"owner_id"`
	Status     string `json:"status"`
	ConfigTOML string `json:"-"`
	Version    string `json:"version"`
	Image      string `json:"image"`
	SHA256     string `json:"sha256"`
	Error      string `json:"error"`
	LogKey     string `json:"log_key"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func scanJob(row interface {
	Scan(dest ...any) error
}) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.FnName, &j.OwnerID, &j.Status, &j.ConfigTOML,
		&j.Version, &j.Image, &j.SHA256, &j.Error, &j.LogKey, &j.CreatedAt, &j.UpdatedAt)
	return j, err
}

const jobColumns = `id, fn_name, owner_id, status, config_toml, version, image, sha256, error, log_key, created_at, updated_at`

// CreateJob enqueues a deploy. Version must already be resolved
// (deploy.ResolveVersion under the per-function lock).
func (s *Store) CreateJob(ownerID, fnName, configTOML, version string) (Job, error) {
	j := Job{
		ID: newID(16), FnName: fnName, OwnerID: ownerID,
		Status: JobQueued, ConfigTOML: configTOML, Version: version,
	}
	_, err := s.db.Exec(
		`INSERT INTO deploy_jobs (id, fn_name, owner_id, status, config_toml, version) VALUES (?, ?, ?, ?, ?, ?)`,
		j.ID, j.FnName, j.OwnerID, j.Status, j.ConfigTOML, j.Version)
	if err != nil {
		return Job{}, err
	}
	return s.GetJob(j.ID)
}

func (s *Store) GetJob(id string) (Job, error) {
	return scanJob(s.db.QueryRow(
		`SELECT `+jobColumns+` FROM deploy_jobs WHERE id = ?`, id))
}

// ListJobs returns a function's jobs, newest first (limit <= 0 means 50).
func (s *Store) ListJobs(fnName string, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT `+jobColumns+` FROM deploy_jobs WHERE fn_name = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`, fnName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// UpdateJobStatus sets status plus any provided fields (version/image/sha on
// success, error on failure), bumping updated_at.
func (s *Store) UpdateJobStatus(id, status, version, image, sha, jobErr string) error {
	_, err := s.db.Exec(
		`UPDATE deploy_jobs SET status = ?, version = ?, image = ?, sha256 = ?, error = ?,
		 updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`,
		status, version, image, sha, jobErr, id)
	return err
}

// SetJobLogKey records where the build log artifact lives.
func (s *Store) SetJobLogKey(id, logKey string) error {
	_, err := s.db.Exec(
		`UPDATE deploy_jobs SET log_key = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`,
		logKey, id)
	return err
}

// ClaimNextJob atomically moves the oldest queued job to building.
// Returns ok=false when the queue is empty.
func (s *Store) ClaimNextJob() (Job, bool) {
	var id string
	if err := s.db.QueryRow(
		`SELECT id FROM deploy_jobs WHERE status = 'queued' ORDER BY created_at, rowid LIMIT 1`).Scan(&id); err != nil {
		return Job{}, false
	}
	res, err := s.db.Exec(
		`UPDATE deploy_jobs SET status = 'building', updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		 WHERE id = ? AND status = 'queued'`, id)
	if err != nil {
		return Job{}, false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Job{}, false
	}
	j, err := s.GetJob(id)
	if err != nil {
		return Job{}, false
	}
	return j, true
}

// RequeueStuck moves jobs left in building (previous crash) back to queued.
// Returns the number requeued.
func (s *Store) RequeueStuck() int {
	res, err := s.db.Exec(
		`UPDATE deploy_jobs SET status = 'queued', updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		 WHERE status = 'building'`)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}
