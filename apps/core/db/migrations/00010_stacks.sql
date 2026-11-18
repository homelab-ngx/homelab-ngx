-- +goose Up
-- Installed service stacks. A stack is a separate NixOS system built from
-- its own repo. `fetched_*` is the newest commit read from the source with
-- its manifest; `approved_*` is the commit whose grants the operator has
-- approved, and the only one the runtime may build. Both manifests are stored
-- as the validated JSON, so an approval is for exactly what was shown.
CREATE TABLE stacks (
    name              TEXT PRIMARY KEY,
    source            TEXT NOT NULL,
    created_at        INTEGER NOT NULL,
    fetched_rev       TEXT NOT NULL,
    fetched_url       TEXT NOT NULL,
    fetched_manifest  TEXT NOT NULL,
    fetched_at        INTEGER NOT NULL,
    approved_rev      TEXT NOT NULL DEFAULT '',
    approved_url      TEXT NOT NULL DEFAULT '',
    approved_manifest TEXT NOT NULL DEFAULT '',
    approved_at       INTEGER NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE stacks;
