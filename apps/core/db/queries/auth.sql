-- name: GetAuthPassword :one
SELECT * FROM auth_password WHERE id = 1;

-- name: InsertAuthPassword :execrows
INSERT INTO auth_password (id, hash, set_at) VALUES (1, ?, ?) ON CONFLICT (id) DO NOTHING;

-- name: SetAuthPassword :exec
INSERT INTO auth_password (id, hash, set_at) VALUES (1, ?, ?)
ON CONFLICT (id) DO UPDATE SET hash = excluded.hash, set_at = excluded.set_at;

-- name: DeleteAuthPassword :exec
DELETE FROM auth_password;

-- name: InsertAuthSession :exec
INSERT INTO auth_sessions (token_hash, created_at, last_seen, expires_at) VALUES (?, ?, ?, ?);

-- name: GetAuthSession :one
SELECT * FROM auth_sessions WHERE token_hash = ?;

-- name: TouchAuthSession :exec
UPDATE auth_sessions SET last_seen = ?, expires_at = ? WHERE token_hash = ?;

-- name: DeleteAuthSession :exec
DELETE FROM auth_sessions WHERE token_hash = ?;

-- name: DeleteAuthSessions :exec
DELETE FROM auth_sessions;

-- name: PruneAuthSessions :exec
DELETE FROM auth_sessions WHERE expires_at < ?;
