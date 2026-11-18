-- +goose Up
-- All timestamps are INTEGER unix milliseconds.

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE service_overrides (
    unit       TEXT PRIMARY KEY,
    enabled    BOOLEAN NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE backup_overrides (
    unit       TEXT PRIMARY KEY,
    enabled    BOOLEAN,
    schedule   TEXT,
    updated_at INTEGER NOT NULL
);

CREATE TABLE pools (
    uuid       TEXT PRIMARY KEY,
    mountpoint TEXT NOT NULL,
    auto_mount BOOLEAN NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    options    TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);

CREATE TABLE user_stopped (
    unit       TEXT PRIMARY KEY,
    stopped_at INTEGER NOT NULL
);

CREATE TABLE backup_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    unit        TEXT NOT NULL,
    trigger     TEXT NOT NULL CHECK (trigger IN ('cron', 'manual', 'imported')),
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    result      TEXT NOT NULL CHECK (result IN ('running', 'success', 'failure', 'skipped', 'unknown')),
    error       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX backup_runs_unit_started ON backup_runs (unit, started_at DESC);

CREATE TABLE service_events (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    unit   TEXT NOT NULL,
    at     INTEGER NOT NULL,
    kind   TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX service_events_unit_at ON service_events (unit, at);
CREATE INDEX service_events_at ON service_events (at);

CREATE TABLE notification_problems (
    key          TEXT PRIMARY KEY,
    title        TEXT NOT NULL,
    opened_at    INTEGER NOT NULL,
    last_sent_at INTEGER NOT NULL,
    count        INTEGER NOT NULL
);

CREATE TABLE notifications (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    at       INTEGER NOT NULL,
    key      TEXT,
    kind     TEXT NOT NULL CHECK (kind IN ('sent', 'opened', 'reminded', 'resolved')),
    severity TEXT NOT NULL DEFAULT '',
    title    TEXT NOT NULL,
    body     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX notifications_at ON notifications (at);

CREATE TABLE notification_deliveries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    notification_id INTEGER NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    at              INTEGER NOT NULL,
    channel         TEXT NOT NULL,
    ok              BOOLEAN NOT NULL,
    error           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX notification_deliveries_notification ON notification_deliveries (notification_id);

CREATE TABLE secret_rotations (
    name       TEXT PRIMARY KEY,
    rotated_at INTEGER NOT NULL
);

CREATE TABLE podman_images (
    name         TEXT PRIMARY KEY,
    image        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    revision_url TEXT NOT NULL DEFAULT '',
    ports_json   TEXT NOT NULL DEFAULT '[]',
    updated_at   INTEGER NOT NULL
);

CREATE TABLE podman_updates (
    name            TEXT PRIMARY KEY,
    has_update      BOOLEAN NOT NULL,
    current_version TEXT NOT NULL DEFAULT '',
    remote_version  TEXT NOT NULL DEFAULT '',
    local_id        TEXT NOT NULL DEFAULT '',
    remote_id       TEXT NOT NULL DEFAULT '',
    checked_at      INTEGER NOT NULL
);

CREATE TABLE digest_cursor (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    delivered_at INTEGER NOT NULL,
    git_sha      TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE digest_cursor;
DROP TABLE podman_updates;
DROP TABLE podman_images;
DROP TABLE secret_rotations;
DROP TABLE notification_deliveries;
DROP TABLE notifications;
DROP TABLE notification_problems;
DROP TABLE service_events;
DROP TABLE backup_runs;
DROP TABLE user_stopped;
DROP TABLE pools;
DROP TABLE backup_overrides;
DROP TABLE service_overrides;
DROP TABLE meta;
