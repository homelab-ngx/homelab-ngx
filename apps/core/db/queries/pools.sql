-- name: ListPools :many
SELECT uuid, mountpoint, auto_mount, name, options, updated_at FROM pools ORDER BY uuid;

-- name: UpsertPool :exec
INSERT INTO pools (uuid, mountpoint, auto_mount, name, options, updated_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (uuid) DO UPDATE SET
    mountpoint = excluded.mountpoint, auto_mount = excluded.auto_mount,
    name = excluded.name, options = excluded.options, updated_at = excluded.updated_at;

-- name: DeletePool :exec
DELETE FROM pools WHERE uuid = ?;
