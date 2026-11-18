-- +goose Up
-- Backups move out of nix: named backups with sources and destinations,
-- all edited in the dashboard, and a run records a result per destination
-- and per folder. The one nix-declared job (b2-backup.service) and its
-- override row go; the operator rebuilds the backups by hand.

CREATE TABLE backup_destinations (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    name                  TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 64),
    kind                  TEXT NOT NULL CHECK (kind IN ('s3', 'rclone', 'restic')),
    -- The kind's settings that are not secret, as JSON.
    config                TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(config)),
    -- s3, rclone: remove files at the destination that are gone on the host.
    delete_removed        BOOLEAN NOT NULL DEFAULT FALSE,
    -- restic: snapshots to keep; all zero keeps every snapshot.
    keep_daily            INTEGER NOT NULL DEFAULT 0 CHECK (keep_daily >= 0),
    keep_weekly           INTEGER NOT NULL DEFAULT 0 CHECK (keep_weekly >= 0),
    keep_monthly          INTEGER NOT NULL DEFAULT 0 CHECK (keep_monthly >= 0),
    keep_yearly           INTEGER NOT NULL DEFAULT 0 CHECK (keep_yearly >= 0),
    -- The kind's secrets as JSON, age-encrypted to the host key.
    secret                BLOB NOT NULL,
    -- restic: when the operator confirmed the password was saved; a run
    -- refuses the destination until then.
    password_confirmed_at INTEGER,
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL
);

CREATE TABLE backups (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 64),
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    -- 5-field cron; empty runs only by hand.
    schedule        TEXT NOT NULL DEFAULT '',
    bwlimit         TEXT NOT NULL DEFAULT '',
    transfers       INTEGER NOT NULL DEFAULT 0 CHECK (transfers >= 0),
    healthcheck_url TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE backup_sources (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    backup_id   INTEGER NOT NULL REFERENCES backups (id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('stack', 'path', 'core')),
    -- kind stack: the stack and the key of its recommended folder.
    stack       TEXT NOT NULL DEFAULT '',
    folder      TEXT NOT NULL DEFAULT '',
    -- kind path: an absolute directory on the host.
    path        TEXT NOT NULL DEFAULT '',
    -- Folder at the destination; NULL mirrors the host path.
    remote_path TEXT,
    -- Extra skip patterns: [{"pattern": ..., "reason": ...}].
    excludes    TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(excludes)),
    -- s3, rclone: a file is unchanged when its size is; for copies whose
    -- modification times were not kept (an upload made with another tool).
    size_only   BOOLEAN NOT NULL DEFAULT FALSE,
    position    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (backup_id, kind, stack, folder, path)
);

CREATE TABLE backup_targets (
    backup_id      INTEGER NOT NULL REFERENCES backups (id) ON DELETE CASCADE,
    destination_id INTEGER NOT NULL REFERENCES backup_destinations (id) ON DELETE RESTRICT,
    position       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (backup_id, destination_id)
);
CREATE INDEX backup_targets_destination ON backup_targets (destination_id);

-- backup_runs is rebuilt: SQLite cannot alter a CHECK. Old rows keep the
-- unit's name; a row still running belongs to a unit that no longer exists.
CREATE TABLE backup_runs_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    backup_id   INTEGER REFERENCES backups (id) ON DELETE SET NULL,
    backup_name TEXT NOT NULL,
    trigger     TEXT NOT NULL CHECK (trigger IN ('cron', 'manual', 'imported')),
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    result      TEXT NOT NULL CHECK (result IN ('running', 'success', 'partial', 'failure', 'cancelled', 'unknown')),
    error       TEXT NOT NULL DEFAULT '',
    files       INTEGER NOT NULL DEFAULT 0,
    bytes       INTEGER NOT NULL DEFAULT 0
);
INSERT INTO backup_runs_new (id, backup_id, backup_name, trigger, started_at, finished_at, result, error)
SELECT id, NULL,
       CASE WHEN unit LIKE '%.service' THEN substr(unit, 1, length(unit) - 8) ELSE unit END,
       trigger, started_at,
       CASE WHEN result = 'running' THEN started_at ELSE finished_at END,
       CASE result WHEN 'running' THEN 'unknown' WHEN 'skipped' THEN 'failure' ELSE result END,
       CASE WHEN result = 'running' THEN 'superseded by the backup rework' ELSE error END
FROM backup_runs;
DROP TABLE backup_runs;
ALTER TABLE backup_runs_new RENAME TO backup_runs;
CREATE INDEX backup_runs_backup_started ON backup_runs (backup_id, started_at DESC);
CREATE INDEX backup_runs_started ON backup_runs (started_at);

CREATE TABLE backup_run_items (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id           INTEGER NOT NULL REFERENCES backup_runs (id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('destination', 'folder')),
    destination_id   INTEGER,
    destination_name TEXT NOT NULL,
    label            TEXT NOT NULL DEFAULT '',
    path             TEXT NOT NULL DEFAULT '',
    position         INTEGER NOT NULL,
    result           TEXT NOT NULL CHECK (result IN ('pending', 'running', 'success', 'partial', 'failure', 'skipped')),
    files            INTEGER NOT NULL DEFAULT 0,
    bytes            INTEGER NOT NULL DEFAULT 0,
    started_at       INTEGER,
    finished_at      INTEGER,
    error            TEXT NOT NULL DEFAULT '',
    note             TEXT NOT NULL DEFAULT ''
);
CREATE INDEX backup_run_items_run ON backup_run_items (run_id, position);

DROP TABLE backup_overrides;

-- +goose Down
-- The old tables come back; backups, destinations and item results do not.
DROP TABLE backup_run_items;
CREATE TABLE backup_runs_old (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    unit        TEXT NOT NULL,
    trigger     TEXT NOT NULL CHECK (trigger IN ('cron', 'manual', 'imported')),
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    result      TEXT NOT NULL CHECK (result IN ('running', 'success', 'failure', 'skipped', 'unknown')),
    error       TEXT NOT NULL DEFAULT ''
);
INSERT INTO backup_runs_old (id, unit, trigger, started_at, finished_at, result, error)
SELECT id, backup_name, trigger, started_at, finished_at,
       CASE result WHEN 'partial' THEN 'failure' WHEN 'cancelled' THEN 'failure' ELSE result END, error
FROM backup_runs WHERE result <> 'running';
DROP TABLE backup_runs;
ALTER TABLE backup_runs_old RENAME TO backup_runs;
CREATE INDEX backup_runs_unit_started ON backup_runs (unit, started_at DESC);
DROP TABLE backup_targets;
DROP TABLE backup_sources;
DROP TABLE backups;
DROP TABLE backup_destinations;
CREATE TABLE backup_overrides (
    unit       TEXT PRIMARY KEY,
    enabled    BOOLEAN,
    schedule   TEXT,
    updated_at INTEGER NOT NULL
);
