-- Operator auth (users/sessions/device login) + per-function API keys +
-- user-scoped function identity (owner_id/slug/auth_mode).

-- +goose Up
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS device_codes (
  code_hash TEXT PRIMARY KEY,
  user_code TEXT NOT NULL UNIQUE,
  user_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending',
  token_hash TEXT NOT NULL DEFAULT '',
  device_name TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS api_keys (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  fn_name TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  prefix TEXT NOT NULL DEFAULT '',
  key_hash TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  revoked_at TEXT NOT NULL DEFAULT '',
  last_used_at TEXT NOT NULL DEFAULT ''
);

ALTER TABLE functions ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE functions ADD COLUMN slug TEXT NOT NULL DEFAULT '';
ALTER TABLE functions ADD COLUMN auth_mode TEXT NOT NULL DEFAULT 'public';
ALTER TABLE versions ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_functions_slug ON functions(slug) WHERE slug != '';
CREATE INDEX IF NOT EXISTS idx_functions_owner ON functions(owner_id);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_fn ON api_keys(owner_id, fn_name);

-- +goose Down
DROP INDEX IF EXISTS idx_api_keys_fn;
DROP INDEX IF EXISTS idx_sessions_user;
DROP INDEX IF EXISTS idx_functions_slug;
DROP INDEX IF EXISTS idx_functions_owner;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS device_codes;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
