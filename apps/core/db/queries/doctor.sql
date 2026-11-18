-- name: ListDoctorStreaks :many
SELECT check_name, fails FROM doctor_streaks ORDER BY check_name;

-- name: UpsertDoctorStreak :exec
INSERT INTO doctor_streaks (check_name, fails, updated_at) VALUES (?, ?, ?)
ON CONFLICT (check_name) DO UPDATE SET fails = excluded.fails, updated_at = excluded.updated_at;

-- name: DeleteDoctorStreak :exec
DELETE FROM doctor_streaks WHERE check_name = ?;
