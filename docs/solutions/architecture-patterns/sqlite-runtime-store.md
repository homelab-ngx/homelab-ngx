---
title: SQLite runtime store with sqlc and goose
date: 2026-10-15
category: architecture-patterns
module: apps/core/internal/store
problem_type: architecture_pattern
component: database
severity: high
applies_when:
  - Adding mutable daemon state
  - Opening the runtime DB from the CLI
  - Changing the schema
tags: [sqlite, sqlc, goose, wal, migrations, snapshot]
---

# SQLite runtime store with sqlc and goose

## Context

Daemon state used to be spread over seven hand-rolled files (`services.yaml`, `state.json`, `notify-state.json`, and others). Each had its own locking and format, none could be queried, and no history was kept: service restarts, backup runs, and sent notifications were forgotten.

## Guidance

- All mutable daemon state lives in `/var/lib/homelab-daemon/homelab.db`. It is on the root disk so that it exists before `/pool` is mounted. Nix config (`/etc`) and secrets (sops) stay as files.
- The driver is `modernc.org/sqlite` (pure Go, no cgo). Migrations are `apps/core/db/migrations/*.sql` (goose), and sqlc also reads them as the schema. Queries are in `db/queries/*.sql`. Timestamps are integer unix milliseconds.
- `store.Open` sets WAL, `busy_timeout(5000)`, `synchronous(NORMAL)`, `foreign_keys(ON)`, and `_txlock=immediate`. The last one makes a write transaction take its lock up front and wait on the busy timeout, instead of failing when it tries to upgrade the lock.
- Only the daemon migrates (`Migrate: true`). The CLI opens the DB with `Migrate: false`: it never creates the file and fails on a schema mismatch. The CLI opens the DB for exactly two things: the notify fallback when the daemon is down, and doctor failure streaks. Everything else goes through the API.
- A daily prune deletes `notifications` (and their deliveries) older than `db.retentionDays` (90). Backup runs older than a year go in the same prune, with their items.
- Tests that need a store use `internal/store/storetest`: `storetest.Open(t)` (or `Path(t)`) migrates one template per test binary and gives each test its own copy, so tests run in parallel without replaying every migration (about 0.4 s each under `-race`). Do not open a fresh migrated store per test.
- An in-daemon cron, not a systemd timer, takes a `VACUUM INTO` snapshot at `db.snapshotSchedule` (`45 2 * * *`) into `/cache/daemon/backups/homelab-<date>.db` and keeps 7. A backup that includes the Core source takes a fresh snapshot just before it copies that directory, so it never depends on the clock; the live WAL file is never copied. The doctor's `db-snapshot` check alerts when the newest snapshot is older than 36 h.

## Why This Matters

- A migration changes the schema version, and the CLI refuses to run against an older or newer DB. Deploy the daemon before relying on the CLI.
- Postgres is already on the host but was rejected: the alert path must not depend on another service, and the state is needed before `/pool` and Postgres are up.
- Also rejected: cgo SQLite (complicates the build), an ORM (hides the SQL), Litestream (the nightly snapshot plus the off-site job is enough), and a systemd timer for snapshots (a second process would need the DB).

## Examples

Reading the DB while the daemon runs is safe in WAL mode. Never write to it by hand:

```bash
sudo sqlite3 /var/lib/homelab-daemon/homelab.db
```

## Related

- [read-only-nix-config-db-overrides.md](read-only-nix-config-db-overrides.md)
