-- +goose Up
-- Peer trusted circle: approvals are signed decisions from the enrolled
-- phone, so a request may park any number of times, each park says what
-- kind of decision it waits for, and a request carries its own spend
-- limits. Not additive: peer_requests is rebuilt, so an older daemon binary
-- does not run against this schema.
--
-- The rebuild runs inside goose's transaction with foreign_keys ON, so it
-- renames the old table first: SQLite then rewrites the old table's own
-- parent_id reference to the old name, the new table's reference names
-- peer_requests from the start, and dropping the old table deletes no row
-- the new one references. peer_nonces, the only other table referencing
-- peer_requests, goes before the rename.
--
-- New columns. park_kind is the kind of the current or last park (step,
-- secret, limit); fingerprint the hex value a decision must name (the step
-- hash, or a SHA-256 the hub derives for limit and secret parks);
-- park_reason the run's bounded, scrubbed reason for the park. spent_usd is
-- the run's cumulative cost and used_seconds the run time so far, against
-- allowance_usd and allowance_seconds; an approved limit park raises the
-- allowance. prompt_id is the approval prompt's id on the hub.
-- approval_cycles counts the rounds, unbounded. notify_channel goes with
-- ntfy and email approvals, and the reason cycle_cap with the cap (an
-- existing cycle_cap row becomes run_failed).
DROP TABLE peer_nonces;
DROP TABLE push_subscriptions;

ALTER TABLE peer_requests RENAME TO peer_requests_old;

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

INSERT INTO peer_requests (
    id, sender, target, text, secret_names, state, attempt, instance, run_token_hash, delivered_at, claimed_at,
    expires_at, approval_cycles, tool, step, step_hash, notified_at, parent_id, reason, summary, result_secrets,
    created_at, updated_at, park_kind, fingerprint
)
SELECT
    id, sender, target, text, secret_names, state, attempt, instance, run_token_hash, delivered_at, claimed_at,
    expires_at, approval_cycles, tool, step, step_hash,
    -- A parked row's old ntfy or email notice is no hub prompt: the sweep
    -- prompts rows with none.
    CASE WHEN state = 'parked' THEN NULL ELSE notified_at END, parent_id,
    CASE reason WHEN 'cycle_cap' THEN 'run_failed' ELSE reason END, summary, result_secrets,
    created_at, updated_at,
    CASE WHEN step_hash IS NULL THEN NULL ELSE 'step' END, step_hash
FROM peer_requests_old;

-- Dropping a table deletes its rows first, and ON DELETE RESTRICT fires per
-- row on the old table's own parent links: unlink them before the drop.
UPDATE peer_requests_old SET parent_id = NULL;
DROP TABLE peer_requests_old;

-- Pushable and running requests per host, and the expiry sweep.
CREATE INDEX peer_requests_target_state ON peer_requests (target, state, expires_at);
CREATE INDEX peer_requests_expires ON peer_requests (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX peer_requests_token ON peer_requests (run_token_hash) WHERE run_token_hash IS NOT NULL;
CREATE INDEX peer_requests_parent ON peer_requests (parent_id) WHERE parent_id IS NOT NULL;

-- +goose Down
-- Best effort: the dropped tables come back empty, rounds above the old cap
-- are clamped to 3, and the new columns are lost.
ALTER TABLE peer_requests RENAME TO peer_requests_new;

CREATE TABLE peer_requests (
    id              TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64),
    sender          TEXT NOT NULL CHECK (length(sender) BETWEEN 1 AND 64),
    target          TEXT NOT NULL CHECK (length(target) BETWEEN 1 AND 64),
    text            TEXT NOT NULL CHECK (length(text) BETWEEN 1 AND 16384),
    secret_names    TEXT NOT NULL DEFAULT '[]' CHECK (length(secret_names) <= 2560 AND json_valid(secret_names)),
    state           TEXT NOT NULL CHECK (state IN ('waiting', 'delivered', 'running', 'parked', 'approved', 'done', 'failed', 'denied', 'expired')),
    attempt         INTEGER NOT NULL DEFAULT 0,
    instance        TEXT CHECK (length(instance) <= 128),
    run_token_hash  TEXT CHECK (length(run_token_hash) <= 64),
    delivered_at    INTEGER,
    claimed_at      INTEGER,
    expires_at      INTEGER,
    approval_cycles INTEGER NOT NULL DEFAULT 0 CHECK (approval_cycles BETWEEN 0 AND 3),
    tool            TEXT CHECK (length(tool) <= 128),
    step            TEXT CHECK (length(step) <= 8192),
    step_hash       TEXT CHECK (length(step_hash) <= 64),
    notified_at     INTEGER,
    notify_channel  TEXT CHECK (notify_channel IN ('ntfy', 'email')),
    parent_id       TEXT CHECK (length(parent_id) <= 64) REFERENCES peer_requests (id) ON DELETE RESTRICT,
    reason          TEXT CHECK (reason IN ('listener_lost', 'timeout', 'cycle_cap', 'run_failed', 'no_output', 'invalid_output', 'gate_unverified')),
    summary         TEXT CHECK (length(summary) <= 4096),
    result_secrets  TEXT NOT NULL DEFAULT '[]' CHECK (length(result_secrets) <= 2560 AND json_valid(result_secrets)),
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    CHECK ((expires_at IS NULL) = (state IN ('done', 'failed', 'denied', 'expired'))),
    CHECK ((reason IS NOT NULL) = (state = 'failed')),
    CHECK (state <> 'running' OR (instance IS NOT NULL AND run_token_hash IS NOT NULL AND claimed_at IS NOT NULL))
);

INSERT INTO peer_requests (
    id, sender, target, text, secret_names, state, attempt, instance, run_token_hash, delivered_at, claimed_at,
    expires_at, approval_cycles, tool, step, step_hash, notified_at, parent_id, reason, summary, result_secrets,
    created_at, updated_at
)
SELECT
    id, sender, target, text, secret_names, state, attempt, instance, run_token_hash, delivered_at, claimed_at,
    expires_at, MIN(approval_cycles, 3), tool, step, step_hash, notified_at, parent_id, reason, summary, result_secrets,
    created_at, updated_at
FROM peer_requests_new;

UPDATE peer_requests_new SET parent_id = NULL;
DROP TABLE peer_requests_new;

CREATE INDEX peer_requests_target_state ON peer_requests (target, state, expires_at);
CREATE INDEX peer_requests_expires ON peer_requests (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX peer_requests_token ON peer_requests (run_token_hash) WHERE run_token_hash IS NOT NULL;
CREATE INDEX peer_requests_parent ON peer_requests (parent_id) WHERE parent_id IS NOT NULL;

CREATE TABLE peer_nonces (
    hash        TEXT PRIMARY KEY CHECK (length(hash) = 64),
    request_id  TEXT NOT NULL CHECK (length(request_id) BETWEEN 1 AND 64) REFERENCES peer_requests (id) ON DELETE CASCADE,
    cycle       INTEGER NOT NULL CHECK (cycle BETWEEN 1 AND 3),
    step_hash   TEXT NOT NULL CHECK (length(step_hash) = 64),
    decision    TEXT NOT NULL CHECK (decision IN ('approve', 'deny')),
    expires_at  INTEGER NOT NULL,
    consumed_at INTEGER
);
CREATE INDEX peer_nonces_request_cycle ON peer_nonces (request_id, cycle);

CREATE TABLE push_subscriptions (
    endpoint   TEXT PRIMARY KEY CHECK (endpoint LIKE 'https://%'),
    p256dh     TEXT NOT NULL,
    auth       TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
