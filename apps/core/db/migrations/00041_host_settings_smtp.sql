-- +goose Up
-- The host SMTP relay configuration lives in its own section. host_settings is
-- rebuilt because SQLite cannot alter a CHECK constraint.
CREATE TABLE host_settings_new (
    section    TEXT PRIMARY KEY CHECK (section IN ('host', 'domain', 'network', 'firewall', 'wireguard', 'tunnel', 'tailscale', 'ddns', 'time', 'console', 'power', 'power_meter', 'smtp')),
    value      TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(value)),
    secret     BLOB,
    updated_at INTEGER NOT NULL
);
INSERT INTO host_settings_new SELECT section, value, secret, updated_at FROM host_settings;
DROP TABLE host_settings;
ALTER TABLE host_settings_new RENAME TO host_settings;

-- +goose Down
DELETE FROM host_settings WHERE section = 'smtp';
CREATE TABLE host_settings_old (
    section    TEXT PRIMARY KEY CHECK (section IN ('host', 'domain', 'network', 'firewall', 'wireguard', 'tunnel', 'tailscale', 'ddns', 'time', 'console', 'power', 'power_meter')),
    value      TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(value)),
    secret     BLOB,
    updated_at INTEGER NOT NULL
);
INSERT INTO host_settings_old SELECT section, value, secret, updated_at FROM host_settings;
DROP TABLE host_settings;
ALTER TABLE host_settings_old RENAME TO host_settings;
