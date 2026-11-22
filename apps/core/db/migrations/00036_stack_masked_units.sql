-- +goose Up
-- `masked_units`: the stack's disabled services, a JSON array of the unit
-- names in its manifest's health check that the operator masked. A masked
-- unit stays stopped and cannot be started until it is enabled again.
ALTER TABLE stacks ADD COLUMN masked_units TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE stacks DROP COLUMN masked_units;
