-- name: GetFunction :one
SELECT name, active_version, config_toml, owner_id, slug, auth_mode FROM functions WHERE name = ?;

-- name: ListFunctions :many
SELECT name, active_version, config_toml, owner_id, slug, auth_mode FROM functions ORDER BY name;

-- name: ListFunctionsByOwner :many
SELECT name, active_version, config_toml, owner_id, slug, auth_mode FROM functions
WHERE owner_id = ? OR owner_id = '' ORDER BY name;

-- name: GetFunctionBySlug :one
SELECT name, active_version, config_toml, owner_id, slug, auth_mode FROM functions WHERE slug = ?;

-- name: GetFunctionNamespaced :one
SELECT f.name, f.active_version, f.config_toml, f.owner_id, f.slug, f.auth_mode
FROM functions f JOIN users u ON u.id = f.owner_id
WHERE u.name = ? AND f.name = ?;

-- name: UpsertFunctionOwned :exec
INSERT INTO functions (name, active_version, config_toml, owner_id, slug, auth_mode)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (name) DO UPDATE SET active_version = excluded.active_version, config_toml = excluded.config_toml,
owner_id = excluded.owner_id, slug = excluded.slug, auth_mode = excluded.auth_mode;

-- name: InsertVersion :exec
INSERT INTO versions (name, ver, sha256, handler_key, config_json, status, owner_id)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetVersion :one
SELECT name, ver, sha256, handler_key, config_json, status, owner_id FROM versions WHERE name = ? AND ver = ?;

-- name: CreateUser :one
INSERT INTO users (id, name, password_hash) VALUES (?, ?, ?) RETURNING id, name, created_at;

-- name: GetUserByName :one
SELECT id, name, created_at FROM users WHERE name = ?;

-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?);

-- name: GetSessionUser :one
SELECT u.id, u.name, u.created_at FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?;

-- name: CreateDevice :exec
INSERT INTO device_codes (code_hash, user_code, device_name, expires_at) VALUES (?, ?, ?, ?);

-- name: ApproveDevice :exec
UPDATE device_codes SET user_id = ?, status = 'approved', token_hash = ? WHERE user_code = ?;

-- name: CreateAPIKey :one
INSERT INTO api_keys (id, owner_id, fn_name, name, prefix, key_hash)
VALUES (?, ?, ?, ?, ?, ?) RETURNING id, owner_id, fn_name, name, prefix, created_at;

-- name: ListAPIKeys :many
SELECT id, owner_id, fn_name, name, prefix, created_at, revoked_at, last_used_at
FROM api_keys WHERE owner_id = ? AND fn_name = ? ORDER BY created_at;

-- name: FindAPIKey :one
SELECT id, owner_id, fn_name, name, prefix, created_at, revoked_at, last_used_at
FROM api_keys WHERE fn_name = ? AND key_hash = ? AND revoked_at = '';

-- name: RevokeAPIKey :exec
UPDATE api_keys SET revoked_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND owner_id = ? AND revoked_at = '';
