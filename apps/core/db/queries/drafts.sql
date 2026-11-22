-- name: ListDrafts :many
SELECT * FROM stack_drafts ORDER BY name;

-- name: GetDraft :one
SELECT * FROM stack_drafts WHERE name = ?;

-- name: InsertDraft :exec
INSERT INTO stack_drafts (name, created_at) VALUES (?, ?);

-- name: DeleteDraft :exec
DELETE FROM stack_drafts WHERE name = ?;

-- name: SetDraftCheck :exec
UPDATE stack_drafts SET check_result = ? WHERE name = ?;

-- name: SetDraftBuild :exec
UPDATE stack_drafts SET build_result = ? WHERE name = ?;

-- name: SetDraftFork :exec
UPDATE stack_drafts SET forked_from = ?, forked_rev = ? WHERE name = ?;
