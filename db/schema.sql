-- Snapshot of the current schema (migrations in internal/store/migrations/ are the source of truth).
CREATE TABLE IF NOT EXISTS functions (
  name TEXT PRIMARY KEY,
  active_version TEXT NOT NULL DEFAULT '',
  config_toml TEXT NOT NULL DEFAULT '',
  owner_id TEXT NOT NULL DEFAULT '',
  slug TEXT NOT NULL DEFAULT '',
  auth_mode TEXT NOT NULL DEFAULT 'public'
);

CREATE TABLE IF NOT EXISTS versions (
  name TEXT NOT NULL,
  ver TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  handler_key TEXT NOT NULL,
  config_json TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'queued',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  owner_id TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (name, ver)
);

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

CREATE UNIQUE INDEX IF NOT EXISTS idx_functions_slug ON functions(slug) WHERE slug != '';
CREATE INDEX IF NOT EXISTS idx_functions_owner ON functions(owner_id);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_fn ON api_keys(owner_id, fn_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_fn_hash ON api_keys(fn_name, key_hash) WHERE revoked_at = '';

CREATE TABLE IF NOT EXISTS deploy_jobs (
  id TEXT PRIMARY KEY,
  fn_name TEXT NOT NULL,
  owner_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'queued',
  config_toml TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  image TEXT NOT NULL DEFAULT '',
  sha256 TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  log_key TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_jobs_fn ON deploy_jobs(fn_name, created_at);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON deploy_jobs(status, created_at);
