-- name: ListStackIDs :many
SELECT name, id, updated_at FROM stack_ids ORDER BY id;

-- name: UpsertStackID :exec
INSERT INTO stack_ids (name, id, updated_at) VALUES (?, ?, ?)
ON CONFLICT (name) DO UPDATE SET id = excluded.id, updated_at = excluded.updated_at;
