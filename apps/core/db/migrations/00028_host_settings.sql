-- +goose Up
-- The image host's own settings (#96), edited on the dashboard and applied
-- by the daemon: one JSON row per section. Secrets (a DNS token, the tunnel's
-- credentials, the WireGuard private key) are sealed to the host's age key
-- and never returned. A NixOS host has no rows: its settings come from its
-- NixOS configuration.
CREATE TABLE host_settings (
    section    TEXT PRIMARY KEY CHECK (section IN ('host', 'domain', 'network', 'firewall', 'wireguard', 'tunnel', 'tailscale')),
    value      TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(value)),
    secret     BLOB,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE host_settings;
