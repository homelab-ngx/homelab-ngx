-- +goose Up
-- #82: the app registry, the weekly digest, the service history it fed and
-- the agent peer hub leave core. The peer hub moves to its own stack and
-- starts fresh there, so its tables and meta keys go with no data
-- migration; so do the approval and maintenance-command state the hub
-- poller and the Home Assistant bridge kept in meta.
DROP TABLE app_checks;
DROP TABLE unit_versions;
DROP TABLE service_events;
DROP TABLE digest_cursor;
DROP INDEX peer_notifications_key;
DROP TABLE peer_message_outcomes;
DROP TABLE peer_messages;
DROP TABLE peer_hosts;
-- Dropping a table deletes its rows first, and the self-reference's ON
-- DELETE RESTRICT fires per row: unlink parents before the drop.
UPDATE peer_requests SET parent_id = NULL;
DROP TABLE peer_requests;
DELETE FROM meta WHERE key IN ('peer_approver', 'hemma_decision_cursor', 'hemma_applied_decisions', 'hemma_command_nonces');

-- +goose Down
-- The tables come back empty; the dropped rows and meta keys do not.
CREATE TABLE service_events (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    unit   TEXT NOT NULL,
    at     INTEGER NOT NULL,
    kind   TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX service_events_unit_at ON service_events (unit, at);
CREATE INDEX service_events_at ON service_events (at);

CREATE TABLE digest_cursor (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    delivered_at INTEGER NOT NULL,
    git_sha      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE unit_versions (
    unit    TEXT PRIMARY KEY,
    ident   TEXT NOT NULL,
    version TEXT NOT NULL,
    seen_at INTEGER NOT NULL
);

CREATE TABLE app_checks (
    app        TEXT PRIMARY KEY,
    version    TEXT NOT NULL DEFAULT '',
    digest     TEXT NOT NULL DEFAULT '',
    rev        TEXT NOT NULL DEFAULT '',
    checked_at INTEGER NOT NULL,
    error      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE peer_hosts (
    name                TEXT PRIMARY KEY CHECK (length(name) BETWEEN 1 AND 64),
    instance            TEXT NOT NULL CHECK (length(instance) <= 128),
    instance_started_at INTEGER NOT NULL,
    last_seen_at        INTEGER NOT NULL,
    acked_seq           INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE peer_messages (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    id         TEXT NOT NULL CHECK (length(id) BETWEEN 1 AND 64),
    kind       TEXT NOT NULL CHECK (kind IN ('status', 'result')),
    request_id TEXT CHECK (length(request_id) <= 64),
    sender     TEXT NOT NULL CHECK (length(sender) BETWEEN 1 AND 64),
    target     TEXT NOT NULL CHECK (length(target) BETWEEN 1 AND 64),
    phase      TEXT CHECK (phase IN ('info', 'started', 'progress', 'blocked', 'done', 'failed')),
    body       TEXT CHECK (length(body) <= 16384),
    created_at INTEGER NOT NULL,
    delivered  BOOLEAN NOT NULL DEFAULT FALSE,
    read       BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (sender, id),
    CHECK (body IS NULL OR kind IN ('status', 'result')),
    CHECK ((kind = 'status') = (phase IS NOT NULL)),
    CHECK (kind <> 'result' OR request_id IS NOT NULL)
);
CREATE INDEX peer_messages_target_seq ON peer_messages (target, seq);
CREATE INDEX peer_messages_target_read ON peer_messages (target, read);
CREATE INDEX peer_messages_request ON peer_messages (request_id) WHERE request_id IS NOT NULL;

CREATE TABLE peer_message_outcomes (
    sender      TEXT NOT NULL CHECK (length(sender) BETWEEN 1 AND 64),
    id          TEXT NOT NULL CHECK (length(id) BETWEEN 1 AND 64),
    kind        TEXT NOT NULL CHECK (kind IN ('request', 'result', 'status', 'secret')),
    target      TEXT NOT NULL CHECK (length(target) BETWEEN 1 AND 64),
    secret_name TEXT CHECK (length(secret_name) <= 128),
    request_id  TEXT CHECK (length(request_id) <= 64),
    outcome     TEXT NOT NULL CHECK (outcome IN ('stored', 'delivered', 'applying', 'applied', 'refused', 'failed')),
    reason      TEXT CHECK (reason IN ('scope', 'not_declared', 'real_value', 'wiped', 'offline', 'no_consumer', 'conflict', 'preflight')),
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (sender, id),
    CHECK (secret_name IS NULL OR kind = 'secret')
);
CREATE INDEX peer_outcomes_created ON peer_message_outcomes (created_at);

CREATE TABLE peer_requests (
    id                TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64),
    sender            TEXT NOT NULL CHECK (length(sender) BETWEEN 1 AND 64),
    target            TEXT NOT NULL CHECK (length(target) BETWEEN 1 AND 64),
    text              TEXT NOT NULL CHECK (length(text) BETWEEN 1 AND 16384),
    secret_names      TEXT NOT NULL DEFAULT '[]' CHECK (length(secret_names) <= 2560 AND json_valid(secret_names)),
    state             TEXT NOT NULL CHECK (state IN ('waiting', 'delivered', 'running', 'parked', 'approved', 'done', 'failed', 'denied', 'expired')),
    attempt           INTEGER NOT NULL DEFAULT 0,
    instance          TEXT CHECK (length(instance) <= 128),
    run_token_hash    TEXT CHECK (length(run_token_hash) <= 64),
    delivered_at      INTEGER,
    claimed_at        INTEGER,
    expires_at        INTEGER,
    approval_cycles   INTEGER NOT NULL DEFAULT 0 CHECK (approval_cycles >= 0),
    tool              TEXT CHECK (length(tool) <= 128),
    step              TEXT CHECK (length(step) <= 8192),
    step_hash         TEXT CHECK (length(step_hash) <= 64),
    notified_at       INTEGER,
    parent_id         TEXT CHECK (length(parent_id) <= 64) REFERENCES peer_requests (id) ON DELETE RESTRICT,
    reason            TEXT CHECK (reason IN ('listener_lost', 'timeout', 'run_failed', 'no_output', 'invalid_output', 'gate_unverified')),
    summary           TEXT CHECK (length(summary) <= 4096),
    result_secrets    TEXT NOT NULL DEFAULT '[]' CHECK (length(result_secrets) <= 2560 AND json_valid(result_secrets)),
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    park_kind         TEXT CHECK (park_kind IN ('step', 'secret', 'limit')),
    fingerprint       TEXT CHECK (length(fingerprint) <= 64),
    park_reason       TEXT CHECK (length(park_reason) <= 300),
    spent_usd         REAL NOT NULL DEFAULT 0 CHECK (spent_usd >= 0),
    used_seconds      INTEGER NOT NULL DEFAULT 0 CHECK (used_seconds >= 0),
    allowance_usd     REAL NOT NULL DEFAULT 10 CHECK (allowance_usd >= 0),
    allowance_seconds INTEGER NOT NULL DEFAULT 7200 CHECK (allowance_seconds >= 0),
    prompt_id         TEXT CHECK (length(prompt_id) <= 128),
    CHECK ((expires_at IS NULL) = (state IN ('done', 'failed', 'denied', 'expired'))),
    CHECK ((reason IS NOT NULL) = (state = 'failed')),
    CHECK (state <> 'running' OR (instance IS NOT NULL AND run_token_hash IS NOT NULL AND claimed_at IS NOT NULL))
);

CREATE INDEX peer_requests_target_state ON peer_requests (target, state, expires_at);
CREATE INDEX peer_requests_expires ON peer_requests (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX peer_requests_token ON peer_requests (run_token_hash) WHERE run_token_hash IS NOT NULL;
CREATE INDEX peer_requests_parent ON peer_requests (parent_id) WHERE parent_id IS NOT NULL;

CREATE INDEX peer_notifications_key ON notifications (key) WHERE key IS NOT NULL;
