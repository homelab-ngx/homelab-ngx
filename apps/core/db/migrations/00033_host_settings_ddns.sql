-- +goose Up
-- Dynamic DNS is a section of its own. host_settings is rebuilt: SQLite
-- cannot alter a CHECK.
CREATE TABLE host_settings_new (
    section    TEXT PRIMARY KEY CHECK (section IN ('host', 'domain', 'network', 'firewall', 'wireguard', 'tunnel', 'tailscale', 'ddns')),
    value      TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(value)),
    secret     BLOB,
    updated_at INTEGER NOT NULL
);
INSERT INTO host_settings_new SELECT section, value, secret, updated_at FROM host_settings;
DROP TABLE host_settings;
ALTER TABLE host_settings_new RENAME TO host_settings;

-- +goose Down
DELETE FROM host_settings WHERE section = 'ddns';
CREATE TABLE host_settings_old (
    section    TEXT PRIMARY KEY CHECK (section IN ('host', 'domain', 'network', 'firewall', 'wireguard', 'tunnel', 'tailscale')),
    value      TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(value)),
    secret     BLOB,
    updated_at INTEGER NOT NULL
);
INSERT INTO host_settings_old SELECT section, value, secret, updated_at FROM host_settings;
DROP TABLE host_settings;
ALTER TABLE host_settings_old RENAME TO host_settings;
