-- +goose Up
-- #87: a digest of what the running container was booted with in
-- /run/secrets and the API env file. A value changed since makes the stack
-- page ask for a restart: services read them only when they start. Empty:
-- booted before this column, so nothing is compared.
ALTER TABLE stacks ADD COLUMN booted_vars TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE stacks DROP COLUMN booted_vars;
