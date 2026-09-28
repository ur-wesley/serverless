-- name: GetFunction :one
SELECT name, active_version, config_toml FROM functions WHERE name = ?;

-- name: ListFunctions :many
SELECT name, active_version, config_toml FROM functions ORDER BY name;

-- name: UpsertFunction :exec
INSERT INTO functions (name, active_version, config_toml)
VALUES (?, ?, ?)
ON CONFLICT (name) DO UPDATE SET active_version = excluded.active_version, config_toml = excluded.config_toml;

-- name: InsertVersion :exec
INSERT INTO versions (name, ver, sha256, handler_key, config_json, status)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetVersion :one
SELECT name, ver, sha256, handler_key, config_json, status FROM versions WHERE name = ? AND ver = ?;
