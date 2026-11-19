-- name: ListPartitions :many
SELECT uuid, name, fstype, read_only, auto_mount, updated_at FROM partitions ORDER BY uuid;

-- name: UpsertPartition :exec
INSERT INTO partitions (uuid, name, fstype, read_only, auto_mount, updated_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (uuid) DO UPDATE SET
    name = excluded.name, fstype = excluded.fstype, read_only = excluded.read_only,
    auto_mount = excluded.auto_mount, updated_at = excluded.updated_at;
