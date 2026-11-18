-- +goose Up
-- Stacks written in the dashboard. Each draft's files are a git repo the
-- daemon keeps under its state directory (drafts/<name>); a row holds what
-- the repo does not: the last check and the last build, as JSON ('' when
-- there was none).
CREATE TABLE stack_drafts (
    name         TEXT PRIMARY KEY,
    created_at   INTEGER NOT NULL,
    check_result TEXT NOT NULL DEFAULT '',
    build_result TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE stack_drafts;
