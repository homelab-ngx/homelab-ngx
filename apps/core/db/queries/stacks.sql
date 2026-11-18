-- name: ListStacks :many
SELECT * FROM stacks ORDER BY name;

-- name: GetStack :one
SELECT * FROM stacks WHERE name = ?;

-- name: InsertStack :exec
INSERT INTO stacks (name, source, created_at, fetched_rev, fetched_url, fetched_manifest, fetched_at)
VALUES (sqlc.arg(name), sqlc.arg(source), sqlc.arg(now), sqlc.arg(rev), sqlc.arg(url), sqlc.arg(manifest), sqlc.arg(now));

-- name: SetStackFetched :exec
UPDATE stacks SET fetched_rev = sqlc.arg(rev), fetched_url = sqlc.arg(url),
    fetched_manifest = sqlc.arg(manifest), fetched_at = sqlc.arg(now)
WHERE name = sqlc.arg(name);

-- Approves the fetched commit, but only if it is still the one the caller
-- saw. Returns 0 rows when a newer fetch replaced it.
-- name: ApproveStackFetched :execrows
UPDATE stacks SET approved_rev = fetched_rev, approved_url = fetched_url,
    approved_manifest = fetched_manifest, approved_at = sqlc.arg(now)
WHERE name = sqlc.arg(name) AND fetched_rev = sqlc.arg(rev);

-- name: DeleteStack :execrows
DELETE FROM stacks WHERE name = ?;

-- name: SetStackWantRunning :exec
UPDATE stacks SET want_running = sqlc.arg(want) WHERE name = sqlc.arg(name);

-- name: SetStackGeneration :exec
UPDATE stacks SET generation = sqlc.arg(generation) WHERE name = sqlc.arg(name);

-- name: SetStackNixpkgsChoice :exec
UPDATE stacks SET nixpkgs = sqlc.arg(nixpkgs), nixpkgs_rev = sqlc.arg(nixpkgs_rev) WHERE name = sqlc.arg(name);

-- name: SetStackNixpkgs :exec
UPDATE stacks SET nixpkgs_rev = sqlc.arg(nixpkgs_rev) WHERE name = sqlc.arg(name);

-- name: SetStackLabel :exec
UPDATE stacks SET label = sqlc.arg(label) WHERE name = sqlc.arg(name);

-- name: SetStackSubnet :exec
UPDATE stacks SET subnet = sqlc.arg(subnet) WHERE name = sqlc.arg(name);

-- A generation nix reused (the same system built again) is recorded again
-- and is no longer failed.
-- name: UpsertStackGeneration :exec
INSERT INTO stack_generations (stack, id, rev, url, manifest, system, built_at, nixpkgs_rev, host_ids)
VALUES (sqlc.arg(stack), sqlc.arg(id), sqlc.arg(rev), sqlc.arg(url), sqlc.arg(manifest), sqlc.arg(system), sqlc.arg(now), sqlc.arg(nixpkgs_rev), sqlc.arg(host_ids))
ON CONFLICT (stack, id) DO UPDATE SET rev = excluded.rev, url = excluded.url,
    manifest = excluded.manifest, system = excluded.system, built_at = excluded.built_at,
    nixpkgs_rev = excluded.nixpkgs_rev, host_ids = excluded.host_ids, failed = 0;

-- name: SetStackHostIDs :exec
UPDATE stacks SET host_ids = sqlc.arg(host_ids) WHERE name = sqlc.arg(name);

-- name: GetStackGeneration :one
SELECT * FROM stack_generations WHERE stack = ? AND id = ?;

-- name: ListStackGenerations :many
SELECT * FROM stack_generations WHERE stack = ? ORDER BY id DESC;

-- The generation a rollback from `id` goes to.
-- name: PreviousStackGeneration :one
SELECT * FROM stack_generations
WHERE stack = sqlc.arg(stack) AND id < sqlc.arg(id) AND failed = 0
ORDER BY id DESC LIMIT 1;

-- name: MarkStackGenerationFailed :exec
UPDATE stack_generations SET failed = 1 WHERE stack = ? AND id = ?;

-- name: DeleteStackGenerations :exec
DELETE FROM stack_generations WHERE stack = ?;

-- name: SetStackOp :exec
UPDATE stacks SET op_kind = sqlc.arg(kind), op_step = sqlc.arg(step), op_prev = sqlc.arg(prev), op_started_at = sqlc.arg(now)
WHERE name = sqlc.arg(name);

-- name: SetStackOpStep :exec
UPDATE stacks SET op_step = sqlc.arg(step) WHERE name = sqlc.arg(name) AND op_kind != '';

-- name: ClearStackOp :exec
UPDATE stacks SET op_kind = '', op_step = '', op_prev = '', op_started_at = 0 WHERE name = ?;

-- Puts a stack back from the lock: its approved commit is also the fetched
-- one, and the subnet it had is kept.
-- name: RestoreStack :exec
INSERT INTO stacks (name, source, created_at, fetched_rev, fetched_url, fetched_manifest, fetched_at,
    approved_rev, approved_url, approved_manifest, approved_at, want_running, subnet, label, nixpkgs_rev, nixpkgs)
VALUES (sqlc.arg(name), sqlc.arg(source), sqlc.arg(now), sqlc.arg(rev), sqlc.arg(url), sqlc.arg(manifest), sqlc.arg(now),
    sqlc.arg(rev), sqlc.arg(url), sqlc.arg(manifest), sqlc.arg(now), sqlc.arg(want_running), sqlc.arg(subnet), sqlc.arg(label), sqlc.arg(nixpkgs_rev), sqlc.arg(nixpkgs));

-- name: SetStackFetchedInfo :exec
UPDATE stacks SET fetched_info = sqlc.arg(info) WHERE name = sqlc.arg(name);

-- name: InsertStackUpdate :one
INSERT INTO stack_updates (stack, kind, rev, from_generation, started_at)
VALUES (sqlc.arg(stack), sqlc.arg(kind), sqlc.arg(rev), sqlc.arg(from_generation), sqlc.arg(now))
RETURNING id;

-- name: SetStackUpdateBuilt :exec
UPDATE stack_updates SET to_generation = sqlc.arg(to_generation), changes = sqlc.arg(changes)
WHERE id = sqlc.arg(id);

-- name: FinishStackUpdate :exec
UPDATE stack_updates SET finished_at = sqlc.arg(now), result = sqlc.arg(result),
    reason = sqlc.arg(reason), log = sqlc.arg(log)
WHERE id = sqlc.arg(id);

-- Attempts a daemon restart cut off; the next daemon cannot know their end.
-- name: InterruptStackUpdates :exec
UPDATE stack_updates SET finished_at = sqlc.arg(now), result = 'interrupted',
    reason = 'the daemon restarted during it'
WHERE result = 'running';

-- name: ListStackUpdates :many
SELECT id, stack, kind, rev, from_generation, to_generation, started_at, finished_at, result, reason, changes
FROM stack_updates
WHERE (sqlc.arg(stack) = '' OR stack = sqlc.arg(stack))
ORDER BY id DESC LIMIT sqlc.arg(lim);

-- name: GetStackUpdate :one
SELECT * FROM stack_updates WHERE id = ?;

-- name: DeleteStackUpdates :exec
DELETE FROM stack_updates WHERE stack = ?;

-- name: SetStackAPITokenHash :exec
UPDATE stacks SET api_token_hash = sqlc.arg(hash) WHERE name = sqlc.arg(name);

-- name: SetStackBootedArgs :exec
UPDATE stacks SET booted_args = sqlc.arg(args) WHERE name = sqlc.arg(name);

-- name: SetStackBootedVars :exec
UPDATE stacks SET booted_vars = sqlc.arg(digest) WHERE name = sqlc.arg(name);
