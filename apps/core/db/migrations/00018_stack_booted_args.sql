-- +goose Up
-- #85: the systemd-run arguments a stack's container was booted with, as a
-- JSON array. An update restarts the container when the new arguments
-- differ (a bind changed); the view says a restart is needed when the stored
-- values give other binds than the container has. Empty: booted before this
-- column, compared by recomputing from the manifest that ran.
ALTER TABLE stacks ADD COLUMN booted_args TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE stacks DROP COLUMN booted_args;
