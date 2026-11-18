-- +goose Up
-- Where notifications go: channels the operator adds on the dashboard. Every
-- enabled channel gets every message. The settings that are not secret are
-- JSON; the credentials are sealed to the host's age key, never returned.
CREATE TABLE notification_channels (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 64),
    kind       TEXT NOT NULL CHECK (kind IN ('ntfy', 'webhook')),
    config     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(config)),
    secret     BLOB NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    created_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE notification_channels;
