-- +goose Up
-- The managed tier is gone: every service runs in a stack, so the daemon no
-- longer boots, restarts, enables or disables host units. Its enabled
-- overrides, user-stopped flags and boot id go, and so does the history only
-- it wrote; the one kind still recorded is `updated`.
DROP TABLE service_overrides;
DROP TABLE user_stopped;
DELETE FROM meta WHERE key = 'boot_id';
DELETE FROM service_events WHERE kind <> 'updated';

-- +goose Down
CREATE TABLE service_overrides (
    unit       TEXT PRIMARY KEY,
    enabled    BOOLEAN NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE user_stopped (
    unit       TEXT PRIMARY KEY,
    stopped_at INTEGER NOT NULL
);
