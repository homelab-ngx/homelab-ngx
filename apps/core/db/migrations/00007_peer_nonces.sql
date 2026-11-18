-- +goose Up
-- Approval nonces. One new table only: an older daemon binary still
-- starts against this schema, and Down/Up round-trips. peer_requests already
-- has notified_at and notify_channel (00006).
--
-- A park mints two nonces for its approval cycle, one per decision, each
-- carried in its own ntfy action URL. Only the SHA-256 of a nonce is stored,
-- never the nonce. A nonce binds a decision to the request, the cycle and the
-- step hash the operator was shown; it authenticates nobody, since the
-- approve and deny routes are dashboard mutations like any other.
--
-- consumed_at marks the decision taken for that cycle: the nonce used, or
-- the matching one when the operator decided on the dashboard. A consumed
-- row is the record that makes a repeated identical decision a no-op and
-- the opposite one a conflict, so the prune removes only expired unconsumed
-- rows; consumed rows go with their request.
CREATE TABLE peer_nonces (
    hash        TEXT PRIMARY KEY CHECK (length(hash) = 64),
    request_id  TEXT NOT NULL CHECK (length(request_id) BETWEEN 1 AND 64) REFERENCES peer_requests (id) ON DELETE CASCADE,
    cycle       INTEGER NOT NULL CHECK (cycle BETWEEN 1 AND 3),
    step_hash   TEXT NOT NULL CHECK (length(step_hash) = 64),
    decision    TEXT NOT NULL CHECK (decision IN ('approve', 'deny')),
    expires_at  INTEGER NOT NULL,
    consumed_at INTEGER
);
-- The decision taken for a request's cycle.
CREATE INDEX peer_nonces_request_cycle ON peer_nonces (request_id, cycle);
-- The scrub's lookup of a request's approval problem in the notification log.
CREATE INDEX peer_notifications_key ON notifications (key) WHERE key IS NOT NULL;

-- +goose Down
DROP INDEX peer_notifications_key;
DROP TABLE peer_nonces;
