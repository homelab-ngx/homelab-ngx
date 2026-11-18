-- +goose Up
-- Agent peer messaging (hub side). New tables only: an older daemon binary
-- still starts against this schema.
--
-- No column may hold a secret value: a secret is a wire-level kind that is
-- never stored as a message row, and its outcome keeps only the secret's
-- name. Every TEXT column is either an enum (CHECK ... IN) or length-bounded;
-- peer_schema_test.go fails on a text column that is neither allowlisted.

-- One row per peer host. instance is the opaque id of the host's current
-- listener and instance_started_at (unix ms) its start time: the newest
-- instance wins, see PeerHello. acked_seq is the highest peer_messages.seq
-- the host has acknowledged.
CREATE TABLE peer_hosts (
    name                TEXT PRIMARY KEY CHECK (length(name) BETWEEN 1 AND 64),
    instance            TEXT NOT NULL CHECK (length(instance) <= 128),
    instance_started_at INTEGER NOT NULL,
    last_seen_at        INTEGER NOT NULL,
    acked_seq           INTEGER NOT NULL DEFAULT 0
);

-- Status and result messages awaiting replay to their target. seq is the
-- replay cursor (AUTOINCREMENT: never reused after a prune). id is chosen by
-- the sender; sender plus id is the idempotency key. request_id is opaque
-- until the request table exists. A status carries a phase; a result answers
-- a request and has no phase.
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
-- A request's bodies are read under the hub's publish mutex.
CREATE INDEX peer_messages_request ON peer_messages (request_id) WHERE request_id IS NOT NULL;

-- The outcome of every accepted message id, kept 7 days. A retry with the
-- same sender and id replays it; a retry whose kind, target or secret name
-- differs is a conflict. A secret apply is written as 'applying' before the
-- fill and finished after; a retry that finds 'applying' re-runs it.
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
-- Listed newest first and pruned by age.
CREATE INDEX peer_outcomes_created ON peer_message_outcomes (created_at);

-- +goose Down
DROP TABLE peer_message_outcomes;
DROP TABLE peer_messages;
DROP TABLE peer_hosts;
