-- +goose Up
-- Agent peer requests. One new table only: an older daemon binary still
-- starts against this schema, and Down/Up round-trips.
--
-- peer_messages.request_id and peer_message_outcomes.request_id stay plain
-- columns, not foreign keys. SQLite can only add a foreign key by rebuilding
-- a table, which needs foreign_keys OFF outside goose's transaction, and
-- both columns legitimately hold ids with no request row: standalone status
-- and refused-secret outcomes carry opaque ids, and a refused secret keeps
-- the request id it named so a retry can be checked for conflict. The prune
-- does the cascade: a message goes once its request is gone and it is past
-- retention (store.Prune).
--
-- No column holds a secret value: secret_names and result_secrets are names
-- only, step and summary are scrubbed by the hub before they are written and
-- rewritten when a value becomes known, and run_token_hash is the
-- SHA-256 of the per-run token, never the token.
--
-- state is the request lifecycle; see the transition table in
-- internal/peer/request.go. expires_at is when the hub ends a non-terminal
-- request nothing moved (24 h after entering waiting, parked or approved;
-- the run timeout plus grace while running) and NULL once terminal.
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
-- Pushable and running requests per host, and the expiry sweep.
CREATE INDEX peer_requests_target_state ON peer_requests (target, state, expires_at);
CREATE INDEX peer_requests_expires ON peer_requests (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX peer_requests_token ON peer_requests (run_token_hash) WHERE run_token_hash IS NOT NULL;
CREATE INDEX peer_requests_parent ON peer_requests (parent_id) WHERE parent_id IS NOT NULL;

-- +goose Down
DROP TABLE peer_requests;
