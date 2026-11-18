-- +goose Up
-- Security audits the operator starts on the Settings page. An audit is
-- asked in chunks, one per area and then a summary; each chunk's material is
-- stored when the audit starts, so a daemon restart continues with the
-- chunks not yet done. JSON columns are '' until there is a value.
CREATE TABLE security_audits (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER NOT NULL DEFAULT 0,
    result      TEXT NOT NULL DEFAULT 'running', -- running | done | partial | failed
    model       TEXT NOT NULL DEFAULT '',
    posture     TEXT NOT NULL DEFAULT '',
    summary     TEXT NOT NULL DEFAULT '',
    first       TEXT NOT NULL DEFAULT '', -- JSON: finding ids to fix first
    error       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE security_audit_chunks (
    audit_id    INTEGER NOT NULL REFERENCES security_audits (id) ON DELETE CASCADE,
    area        TEXT NOT NULL,
    ord         INTEGER NOT NULL,
    state       TEXT NOT NULL DEFAULT 'pending', -- pending | running | done | failed
    attempts    INTEGER NOT NULL DEFAULT 0,
    material    TEXT NOT NULL DEFAULT '',
    findings    TEXT NOT NULL DEFAULT '', -- JSON
    strengths   TEXT NOT NULL DEFAULT '', -- JSON
    raw         TEXT NOT NULL DEFAULT '',
    model       TEXT NOT NULL DEFAULT '',
    usage       TEXT NOT NULL DEFAULT '', -- JSON
    error       TEXT NOT NULL DEFAULT '',
    started_at  INTEGER NOT NULL DEFAULT 0,
    finished_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (audit_id, area)
);

-- +goose Down
DROP TABLE security_audit_chunks;
DROP TABLE security_audits;
