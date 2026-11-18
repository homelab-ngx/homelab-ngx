-- +goose Up
-- The dashboard's one password and its sign-in sessions. Only the password's
-- argon2id hash and each session token's SHA-256 are kept.
CREATE TABLE auth_password (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    hash   TEXT NOT NULL,
    set_at INTEGER NOT NULL
);

CREATE TABLE auth_sessions (
    token_hash TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE auth_sessions;
DROP TABLE auth_password;
