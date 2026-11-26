-- +goose Up
-- The SSH key name (under Host.SSHKeysDir, e.g. "host" or "gh-<hash>")
-- used to fetch and build a private stack repository. Empty uses no key
-- (public repository or the host's GitHub token fallback).
ALTER TABLE stacks ADD COLUMN ssh_key TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE stacks DROP COLUMN ssh_key;
