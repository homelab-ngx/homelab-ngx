-- +goose Up
-- Notification events, destination paths, per-channel filter tracking, and
-- baseline read marker so upgrading hosts start with existing entries read.
ALTER TABLE notifications ADD COLUMN event TEXT NOT NULL DEFAULT '';
ALTER TABLE notifications ADD COLUMN path TEXT NOT NULL DEFAULT '';
ALTER TABLE notification_deliveries ADD COLUMN filtered BOOLEAN NOT NULL DEFAULT FALSE;
INSERT OR IGNORE INTO meta (key, value)
SELECT 'notifications.read_up_to', CAST(COALESCE(MAX(id), 0) AS TEXT) FROM notifications;

-- +goose Down
DELETE FROM meta WHERE key = 'notifications.read_up_to';
ALTER TABLE notification_deliveries DROP COLUMN filtered;
ALTER TABLE notifications DROP COLUMN path;
ALTER TABLE notifications DROP COLUMN event;
