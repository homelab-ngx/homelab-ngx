-- name: ListDrafts :many
SELECT name, created_at, check_result, build_result FROM stack_drafts ORDER BY name;

-- name: GetDraft :one
SELECT name, created_at, check_result, build_result FROM stack_drafts WHERE name = ?;

-- name: InsertDraft :exec
INSERT INTO stack_drafts (name, created_at) VALUES (?, ?);

-- name: DeleteDraft :exec
DELETE FROM stack_drafts WHERE name = ?;

-- name: SetDraftCheck :exec
UPDATE stack_drafts SET check_result = ? WHERE name = ?;

-- name: SetDraftBuild :exec
UPDATE stack_drafts SET build_result = ? WHERE name = ?;
