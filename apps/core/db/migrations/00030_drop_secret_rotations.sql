-- +goose Up
-- The host keeps no secrets registry any more: what a stack needs is a stack
-- variable, and the daemon's own secrets are sealed in their own rows.
DROP TABLE secret_rotations;

-- +goose Down
CREATE TABLE secret_rotations (
    name       TEXT PRIMARY KEY,
    rotated_at INTEGER NOT NULL
);
