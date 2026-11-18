-- +goose Up
-- What the last fetch learned about the source beyond its manifest, as JSON:
-- the commits since the running generation, whether a local checkout had
-- uncommitted changes, and the sops files with their age recipients.
ALTER TABLE stacks ADD COLUMN fetched_info TEXT NOT NULL DEFAULT '';

-- One install, update or rollback of a stack: every attempt, including the
-- ones that failed to build or were rolled back. `result` is 'running' until
-- it ends, then 'healthy', 'rolled_back', 'build_failed', 'failed' or
-- 'interrupted'. `changes` is the package diff from the generation before,
-- as JSON. `log` is the phases and the build output, capped.
CREATE TABLE stack_updates (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    stack           TEXT    NOT NULL,
    kind            TEXT    NOT NULL,
    rev             TEXT    NOT NULL DEFAULT '',
    from_generation INTEGER NOT NULL DEFAULT 0,
    to_generation   INTEGER NOT NULL DEFAULT 0,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER NOT NULL DEFAULT 0,
    result          TEXT    NOT NULL DEFAULT 'running',
    reason          TEXT    NOT NULL DEFAULT '',
    changes         TEXT    NOT NULL DEFAULT '[]',
    log             TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX stack_updates_stack ON stack_updates (stack, id);

-- +goose Down
DROP TABLE stack_updates;
ALTER TABLE stacks DROP COLUMN fetched_info;
