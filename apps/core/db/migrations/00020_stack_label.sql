-- +goose Up
-- The name the dashboard shows for a stack, set by the operator. Empty: the
-- stack's name. The name itself is fixed at install: it keys the container,
-- its units, its state and everything else the daemon keeps for it.
ALTER TABLE stacks ADD COLUMN label TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE stacks DROP COLUMN label;
