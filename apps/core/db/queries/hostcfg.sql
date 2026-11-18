-- name: ListHostSettings :many
SELECT * FROM host_settings ORDER BY section;

-- name: UpsertHostSetting :exec
INSERT INTO host_settings (section, value, secret, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (section) DO UPDATE SET value = excluded.value, secret = excluded.secret, updated_at = excluded.updated_at;
