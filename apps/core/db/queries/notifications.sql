-- name: ListNotificationProblems :many
SELECT * FROM notification_problems ORDER BY opened_at, key;

-- name: UpsertNotificationProblem :exec
INSERT INTO notification_problems (key, title, opened_at, last_sent_at, count) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (key) DO UPDATE SET
    title = excluded.title, opened_at = excluded.opened_at,
    last_sent_at = excluded.last_sent_at, count = excluded.count;

-- name: DeleteNotificationProblem :exec
DELETE FROM notification_problems WHERE key = ?;

-- name: InsertNotification :one
INSERT INTO notifications (at, key, kind, event, path, severity, title, body)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: ListNotifications :many
SELECT * FROM notifications ORDER BY at DESC, id DESC LIMIT ?;

-- name: PruneNotifications :execrows
DELETE FROM notifications WHERE at < ?;

-- name: InsertNotificationDelivery :exec
INSERT INTO notification_deliveries (notification_id, at, channel, ok, filtered, error)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListNotificationDeliveries :many
SELECT * FROM notification_deliveries WHERE notification_id IN (sqlc.slice(ids))
ORDER BY notification_id, id;

-- name: ListNotificationsBefore :many
SELECT * FROM notifications WHERE id < ? ORDER BY id DESC LIMIT ?;

-- name: CountUnreadNotifications :one
SELECT COUNT(*) FROM notifications n
WHERE n.id > ?
  AND NOT (
    n.kind IN ('opened', 'reminded')
    AND n.key IS NOT NULL
    AND EXISTS (SELECT 1 FROM notification_problems p WHERE p.key = n.key)
  );

-- name: LatestNotificationID :one
SELECT CAST(COALESCE(MAX(id), 0) AS INTEGER) FROM notifications;

-- name: ListNotificationChannels :many
SELECT * FROM notification_channels ORDER BY name;

-- name: GetNotificationChannel :one
SELECT * FROM notification_channels WHERE id = ?;

-- name: InsertNotificationChannel :one
INSERT INTO notification_channels (name, kind, config, secret, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateNotificationChannel :one
UPDATE notification_channels SET name = ?, config = ?, secret = ?, enabled = ? WHERE id = ?
RETURNING *;

-- name: DeleteNotificationChannel :execrows
DELETE FROM notification_channels WHERE id = ?;
