-- name: InsertSecurityAudit :one
INSERT INTO security_audits (started_at, model) VALUES (?, ?) RETURNING id;

-- name: InsertSecurityAuditChunk :exec
INSERT INTO security_audit_chunks (audit_id, area, ord, material) VALUES (?, ?, ?, ?);

-- name: ListSecurityAudits :many
SELECT * FROM security_audits ORDER BY id DESC LIMIT ?;

-- name: GetSecurityAudit :one
SELECT * FROM security_audits WHERE id = ?;

-- name: ListSecurityAuditChunks :many
SELECT * FROM security_audit_chunks WHERE audit_id = ? ORDER BY ord;

-- The light view of the chunks, for the list: no material or raw answer.
-- name: ListSecurityAuditChunkStates :many
SELECT area, ord, state, findings, usage FROM security_audit_chunks WHERE audit_id = ? ORDER BY ord;

-- name: RunningSecurityAudits :many
SELECT id FROM security_audits WHERE result = 'running' ORDER BY id;

-- name: StartSecurityAuditChunk :exec
UPDATE security_audit_chunks SET state = 'running', attempts = attempts + 1, started_at = sqlc.arg(now)
WHERE audit_id = sqlc.arg(audit_id) AND area = sqlc.arg(area);

-- name: SetSecurityAuditChunkMaterial :exec
UPDATE security_audit_chunks SET material = sqlc.arg(material)
WHERE audit_id = sqlc.arg(audit_id) AND area = sqlc.arg(area);

-- A failed attempt that will be tried again goes back to pending.
-- name: FinishSecurityAuditChunk :exec
UPDATE security_audit_chunks SET state = sqlc.arg(state), findings = sqlc.arg(findings),
    strengths = sqlc.arg(strengths), raw = sqlc.arg(raw), model = sqlc.arg(model), usage = sqlc.arg(usage),
    error = sqlc.arg(error), finished_at = sqlc.arg(now)
WHERE audit_id = sqlc.arg(audit_id) AND area = sqlc.arg(area);

-- name: FinishSecurityAudit :exec
UPDATE security_audits SET finished_at = sqlc.arg(now), result = sqlc.arg(result), posture = sqlc.arg(posture),
    summary = sqlc.arg(summary), first = sqlc.arg(first), error = sqlc.arg(error)
WHERE id = sqlc.arg(id);

-- name: PruneSecurityAudits :exec
DELETE FROM security_audits WHERE id NOT IN (SELECT id FROM security_audits ORDER BY id DESC LIMIT ?);
