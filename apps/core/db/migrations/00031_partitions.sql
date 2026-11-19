-- +goose Up
-- A partition mounted from the Storage page: a file system that is not a
-- bcachefs pool, kept by its UUID so it mounts again at boot under the same
-- name (/var/mnt/<name>).
CREATE TABLE partitions (
    uuid       TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    fstype     TEXT NOT NULL,
    read_only  BOOLEAN NOT NULL,
    auto_mount BOOLEAN NOT NULL,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE partitions;
