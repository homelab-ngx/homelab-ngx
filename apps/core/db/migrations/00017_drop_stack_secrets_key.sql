-- +goose Up
-- #84: a stack's secrets are stack variables the operator fills in on the
-- host (variables.age in its state directory), not sops files encrypted to a
-- per-stack age key. The public half of that key goes.
ALTER TABLE stacks DROP COLUMN secrets_key;

-- +goose Down
-- The column comes back empty; the keys themselves were files, not rows.
ALTER TABLE stacks ADD COLUMN secrets_key TEXT NOT NULL DEFAULT '';
