-- name: ListSecretRotations :many
SELECT name, rotated_at FROM secret_rotations ORDER BY name;

-- name: SetSecretRotation :exec
INSERT INTO secret_rotations (name, rotated_at) VALUES (?, ?)
ON CONFLICT (name) DO UPDATE SET rotated_at = excluded.rotated_at;

-- name: DeleteSecretRotation :exec
DELETE FROM secret_rotations WHERE name = ?;
