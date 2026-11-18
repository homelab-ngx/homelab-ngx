-- +goose Up
-- The latest availability check per app: the newest candidate
-- within the app's track. A failed check keeps the last candidate and
-- records the error. Whether it is newer than the deployed pin is computed
-- on read, so a deploy clears it without a re-check.
CREATE TABLE app_checks (
    app        TEXT PRIMARY KEY,
    version    TEXT NOT NULL DEFAULT '',
    digest     TEXT NOT NULL DEFAULT '',
    rev        TEXT NOT NULL DEFAULT '',
    checked_at INTEGER NOT NULL,
    error      TEXT NOT NULL DEFAULT ''
);

-- Replaced by app_checks (update state) and a live podman read (metadata).
DROP TABLE podman_updates;
DROP TABLE podman_images;

-- Versions now come from the apps in config.json, with a different ident
-- than the runtime inference stored: start from a fresh baseline instead of
-- recording a spurious update for every unit.
DELETE FROM unit_versions;

-- +goose Down
DROP TABLE app_checks;

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
