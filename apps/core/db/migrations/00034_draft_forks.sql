-- +goose Up
-- A draft forked from an installed stack records where its first commit
-- came from: the stack's source and the commit copied ('' for a draft
-- started from the template).
ALTER TABLE stack_drafts ADD COLUMN forked_from TEXT NOT NULL DEFAULT '';
ALTER TABLE stack_drafts ADD COLUMN forked_rev TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE stack_drafts DROP COLUMN forked_rev;
ALTER TABLE stack_drafts DROP COLUMN forked_from;
