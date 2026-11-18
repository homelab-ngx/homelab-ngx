-- +goose Up
-- Browsers subscribed to Web Push (the installed dashboard or a Chrome tab).
-- endpoint is the push service URL the browser handed out; p256dh and auth
-- are its base64url message-encryption keys. A 404/410 from the push service
-- deletes the row. The VAPID signing key lives in meta (webpush_vapid_key).
CREATE TABLE push_subscriptions (
    endpoint   TEXT PRIMARY KEY CHECK (endpoint LIKE 'https://%'),
    p256dh     TEXT NOT NULL,
    auth       TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE push_subscriptions;
