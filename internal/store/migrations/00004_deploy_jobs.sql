-- Async deploy jobs: POST /deploy enqueues, a background worker executes.
-- status: queued -> building -> active | failed.

-- +goose Up
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

-- +goose Down
DROP INDEX IF EXISTS idx_jobs_status;
DROP INDEX IF EXISTS idx_jobs_fn;
DROP TABLE IF EXISTS deploy_jobs;
