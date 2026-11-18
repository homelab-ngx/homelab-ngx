-- +goose Up
-- An install or update in flight, so a daemon that restarts in the middle
-- of one can finish it. `op_kind` is 'start' (an install: the first build)
-- or 'update', '' when nothing is in flight. `op_step` is 'building' until
-- the new generation is the stack's current one, then 'activating'.
-- `op_prev` is the generation row to go back to, as JSON ('' for an
-- install): a copy, because a commit that builds the same system reuses
-- the generation and overwrites its row.
ALTER TABLE stacks ADD COLUMN op_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE stacks ADD COLUMN op_step TEXT NOT NULL DEFAULT '';
ALTER TABLE stacks ADD COLUMN op_prev TEXT NOT NULL DEFAULT '';
ALTER TABLE stacks ADD COLUMN op_started_at INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE stacks DROP COLUMN op_started_at;
ALTER TABLE stacks DROP COLUMN op_prev;
ALTER TABLE stacks DROP COLUMN op_step;
ALTER TABLE stacks DROP COLUMN op_kind;
