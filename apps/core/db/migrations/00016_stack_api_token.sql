-- +goose Up
-- #82: a stack whose approved manifest has an `api` scope gets a bearer
-- token for the stack listener. Only its SHA-256 is kept; the plain token
-- lives in the stack's state directory. Empty: no token.
ALTER TABLE stacks ADD COLUMN api_token_hash TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE stacks DROP COLUMN api_token_hash;
