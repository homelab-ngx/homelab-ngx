-- ---- destinations ----

-- name: ListBackupDestinations :many
SELECT * FROM backup_destinations ORDER BY name;

-- name: GetBackupDestination :one
SELECT * FROM backup_destinations WHERE id = ?;

-- name: InsertBackupDestination :one
INSERT INTO backup_destinations (name, kind, config, delete_removed, keep_daily, keep_weekly, keep_monthly,
    keep_yearly, secret, password_confirmed_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: UpdateBackupDestination :exec
UPDATE backup_destinations SET name = ?, config = ?, delete_removed = ?, keep_daily = ?, keep_weekly = ?,
    keep_monthly = ?, keep_yearly = ?, secret = ?, updated_at = ?
WHERE id = ?;

-- name: ConfirmBackupDestinationPassword :exec
UPDATE backup_destinations SET password_confirmed_at = ?, updated_at = ? WHERE id = ?;

-- name: DeleteBackupDestination :exec
DELETE FROM backup_destinations WHERE id = ?;

-- name: CountBackupDestinationUses :one
SELECT count(*) FROM backup_targets WHERE destination_id = ?;

-- ---- backups ----

-- name: ListBackups :many
SELECT * FROM backups ORDER BY name;

-- name: GetBackup :one
SELECT * FROM backups WHERE id = ?;

-- name: InsertBackup :one
INSERT INTO backups (name, enabled, schedule, bwlimit, transfers, healthcheck_url, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: UpdateBackup :exec
UPDATE backups SET name = ?, enabled = ?, schedule = ?, bwlimit = ?, transfers = ?, healthcheck_url = ?,
    updated_at = ?
WHERE id = ?;

-- name: DeleteBackup :exec
DELETE FROM backups WHERE id = ?;

-- name: ListBackupSources :many
SELECT * FROM backup_sources ORDER BY backup_id, position, id;

-- name: ListBackupSourcesOf :many
SELECT * FROM backup_sources WHERE backup_id = ? ORDER BY position, id;

-- name: DeleteBackupSources :exec
DELETE FROM backup_sources WHERE backup_id = ?;

-- name: InsertBackupSource :exec
INSERT INTO backup_sources (backup_id, kind, stack, folder, path, remote_path, excludes, size_only, position)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListBackupTargets :many
SELECT * FROM backup_targets ORDER BY backup_id, position, destination_id;

-- name: ListBackupTargetsOf :many
SELECT * FROM backup_targets WHERE backup_id = ? ORDER BY position, destination_id;

-- name: DeleteBackupTargets :exec
DELETE FROM backup_targets WHERE backup_id = ?;

-- name: InsertBackupTarget :exec
INSERT INTO backup_targets (backup_id, destination_id, position) VALUES (?, ?, ?);

-- ---- runs ----

-- name: InsertBackupRun :one
INSERT INTO backup_runs (backup_id, backup_name, trigger, started_at, result) VALUES (?, ?, ?, ?, 'running')
RETURNING id;

-- name: FinishBackupRun :exec
UPDATE backup_runs SET finished_at = ?, result = ?, error = ?, files = ?, bytes = ? WHERE id = ?;

-- name: GetBackupRun :one
SELECT * FROM backup_runs WHERE id = ?;

-- LastBackupRuns returns the newest run of every backup.
-- name: LastBackupRuns :many
SELECT * FROM backup_runs
WHERE id IN (
    SELECT (
        SELECT r.id FROM backup_runs r WHERE r.backup_id = b.id
        ORDER BY r.started_at DESC, r.id DESC LIMIT 1
    )
    FROM backups b
);

-- name: LastSuccessfulBackupRuns :many
SELECT * FROM backup_runs
WHERE id IN (
    SELECT (
        SELECT r.id FROM backup_runs r WHERE r.backup_id = b.id AND r.result = 'success'
        ORDER BY r.finished_at DESC, r.id DESC LIMIT 1
    )
    FROM backups b
);

-- RecentBackupRuns returns every backup's runs started since a time, oldest
-- first, for the overview's history strip.
-- name: RecentBackupRuns :many
SELECT * FROM backup_runs WHERE backup_id IS NOT NULL AND started_at >= ? ORDER BY started_at, id;

-- name: ListBackupRuns :many
SELECT * FROM backup_runs WHERE backup_id = ? ORDER BY started_at DESC, id DESC LIMIT ?;

-- RunningBackupRuns returns the rows still in flight, newest first per backup.
-- name: RunningBackupRuns :many
SELECT * FROM backup_runs WHERE result = 'running' ORDER BY backup_id, started_at DESC, id DESC;

-- name: PruneBackupRuns :execrows
DELETE FROM backup_runs WHERE started_at < ? AND result <> 'running';

-- name: ListBackupRunItems :many
SELECT * FROM backup_run_items WHERE run_id = ? ORDER BY position, id;

-- name: DeleteBackupRunItems :exec
DELETE FROM backup_run_items WHERE run_id = ?;

-- name: InsertBackupRunItem :exec
INSERT INTO backup_run_items (run_id, kind, destination_id, destination_name, label, path, position, result,
    files, bytes, started_at, finished_at, error, note)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- ---- previews ----

-- StartBackupPreview replaces the backup's preview with one in flight.
-- name: StartBackupPreview :exec
INSERT INTO backup_previews (backup_id, started_at, result) VALUES (?, ?, 'running')
ON CONFLICT (backup_id) DO UPDATE SET
    started_at = excluded.started_at, finished_at = NULL, result = 'running', error = '', items = '[]';

-- name: FinishBackupPreview :exec
UPDATE backup_previews SET finished_at = ?, result = ?, error = ?, items = ? WHERE backup_id = ?;

-- name: GetBackupPreview :one
SELECT * FROM backup_previews WHERE backup_id = ?;

-- name: RunningBackupPreviews :many
SELECT * FROM backup_previews WHERE result = 'running' ORDER BY backup_id;

-- name: DeleteBackupPreview :exec
DELETE FROM backup_previews WHERE backup_id = ?;

-- name: ListBackupPreviewIDs :many
SELECT backup_id FROM backup_previews;
