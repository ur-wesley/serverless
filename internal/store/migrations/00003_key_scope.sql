-- Scope API keys: fast (fn_name, key_hash) lookup for non-revoked keys.
-- Ownership itself is enforced in controlauth.CheckKey (key owner must match
-- function owner); global function-name uniqueness stays the v1 rule.

-- +goose Up
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_fn_hash ON api_keys(fn_name, key_hash) WHERE revoked_at = '';

-- +goose Down
DROP INDEX IF EXISTS idx_api_keys_fn_hash;
