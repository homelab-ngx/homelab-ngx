-- +goose Up
-- The last version seen running per managed unit, so a version change is
-- noticed even when the deploy that made it also restarted the daemon.
-- ident changes on every update (nix store path or container image ID);
-- version is the human label shown in history.
CREATE TABLE unit_versions (
    unit    TEXT PRIMARY KEY,
    ident   TEXT NOT NULL,
    version TEXT NOT NULL,
    seen_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE unit_versions;
