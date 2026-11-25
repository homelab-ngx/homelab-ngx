-- +goose Up
-- A preview compares a backup's folders with its destinations and copies
-- nothing. Only the newest one of a backup is kept, outside its run history.
CREATE TABLE backup_previews (
    backup_id   INTEGER PRIMARY KEY REFERENCES backups (id) ON DELETE CASCADE,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    result      TEXT NOT NULL CHECK (result IN ('running', 'success', 'partial', 'failure', 'cancelled', 'unknown')),
    error       TEXT NOT NULL DEFAULT '',
    -- The runner's items with what a run would do to each folder, as JSON.
    items       TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(items))
);

-- An item that was not done when its run was stopped is cancelled.
-- backup_run_items is rebuilt: SQLite cannot alter a CHECK.
CREATE TABLE backup_run_items_new (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id           INTEGER NOT NULL REFERENCES backup_runs (id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('destination', 'folder')),
    destination_id   INTEGER,
    destination_name TEXT NOT NULL,
    label            TEXT NOT NULL DEFAULT '',
    path             TEXT NOT NULL DEFAULT '',
    position         INTEGER NOT NULL,
    result           TEXT NOT NULL CHECK (result IN ('pending', 'running', 'success', 'partial', 'failure', 'skipped', 'cancelled')),
    files            INTEGER NOT NULL DEFAULT 0,
    bytes            INTEGER NOT NULL DEFAULT 0,
    started_at       INTEGER,
    finished_at      INTEGER,
    error            TEXT NOT NULL DEFAULT '',
    note             TEXT NOT NULL DEFAULT ''
);
INSERT INTO backup_run_items_new
SELECT id, run_id, kind, destination_id, destination_name, label, path, position, result, files, bytes,
       started_at, finished_at, error, note
FROM backup_run_items;
DROP TABLE backup_run_items;
ALTER TABLE backup_run_items_new RENAME TO backup_run_items;
CREATE INDEX backup_run_items_run ON backup_run_items (run_id, position);

-- +goose Down
CREATE TABLE backup_run_items_old (
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
INSERT INTO backup_run_items_old
SELECT id, run_id, kind, destination_id, destination_name, label, path, position,
       CASE result WHEN 'cancelled' THEN 'skipped' ELSE result END, files, bytes,
       started_at, finished_at, error, note
FROM backup_run_items;
DROP TABLE backup_run_items;
ALTER TABLE backup_run_items_old RENAME TO backup_run_items;
CREATE INDEX backup_run_items_run ON backup_run_items (run_id, position);
DROP TABLE backup_previews;
