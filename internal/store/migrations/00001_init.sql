-- Baseline schema: functions + versions as of the pre-auth releases.
-- Later changes live in sequentially numbered files; never edit this one.

-- +goose Up
CREATE TABLE IF NOT EXISTS functions (
  name TEXT PRIMARY KEY,
  active_version TEXT NOT NULL DEFAULT '',
  config_toml TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS versions (
  name TEXT NOT NULL,
  ver TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  handler_key TEXT NOT NULL,
  config_json TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'queued',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  PRIMARY KEY (name, ver)
);

-- +goose Down
DROP TABLE IF EXISTS versions;
DROP TABLE IF EXISTS functions;
