-- +goose Up
-- The host's ids for the users and groups its stacks declare: one number per
-- name, from 2000 to 2099, given to the folders the daemon makes for a
-- stack's path variables. A user and a group of the same name share it, and
-- so does every stack that declares the name.
CREATE TABLE stack_ids (
    name       TEXT PRIMARY KEY,
    id         INTEGER NOT NULL UNIQUE,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE stack_ids;
