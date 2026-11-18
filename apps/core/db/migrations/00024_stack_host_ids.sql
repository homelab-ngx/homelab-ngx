-- +goose Up
-- Manifest version 8: the uid and gid a stack's users and groups take from
-- the host directories of its path variables. A generation records the ids
-- it was built with; the stack's row holds the ids its folders have now. When
-- they differ, an update builds the stack again. Empty: none (before
-- version 8, or no path names an owner or group).
ALTER TABLE stacks ADD COLUMN host_ids TEXT NOT NULL DEFAULT '';
ALTER TABLE stack_generations ADD COLUMN host_ids TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE stack_generations DROP COLUMN host_ids;
ALTER TABLE stacks DROP COLUMN host_ids;
