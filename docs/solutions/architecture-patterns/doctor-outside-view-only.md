---
title: Doctor checks only what the daemon cannot see
date: 2026-10-15
category: architecture-patterns
module: apps/core/internal/doctor
problem_type: architecture_pattern
component: observability
severity: medium
applies_when:
  - Adding a health check or an alert
  - Debugging a doctor or daily-audit alert
  - Making a unit alert on failure
tags: [doctor, health-audit, notifications, ntfy, alerting, churn]
---

# Doctor checks only what the daemon cannot see

## Context

The doctor had grown to 17 checks and about 1,000 lines. Two journal-reading checks charged 566 MB of page cache to its unit. Most failures over 30 days were noise: units restarting seconds after activation, a log-volume threshold, immich CPU, and notification delivery errors. The doctor also asked the daemon's own socket about the daemon's services, so a daemon outage and the check for it failed together.

## Guidance

- `homelab doctor` lives in the CLI and works when the daemon is down. It runs seven checks: `daemon`, `caddy`, `disk`, `systemd-units` (failed units the daemon does **not** supervise), `smart`, `cloudflare-tunnel`, and `db-snapshot`.
- Each check returns `ok`, `fail`, or `unknown`. A 5 s per-check timeout or the 10 s run budget yields `unknown`. `unknown` never opens or resolves an alert, and `--fail-on-error` exits 1 only on `fail`.
- `homelab doctor daily` runs at 08:00 from `homelab-health-audit.timer`. It keeps consecutive-`fail` counts in `doctor_streaks`. Two failed runs in a row open the `audit:<check>` problem, and the next `ok` resolves it. A completed audit pings Healthchecks.io (`healthcheck/DAILY_AUDIT_UUID`) even when it found failures. An audit error pings `/fail`.
- The doctor does no expensive reads: no journal, `/proc`, or store scans, and its unit runs with `MemoryMax=64M`.
- The daemon alerts on everything it supervises. Its monitor reads systemd `NRestarts` every minute and opens `churn:<unit>` when a unit restarted at least five times in an hour.
- All alerts go through `internal/notifications`, which delivers them to every enabled notification channel (ntfy, or a webhook); one channel taking it is enough. A problem key (`audit:`, `unit:`, `backup:`, `churn:`) produces one push when the problem opens, one reminder every 24 h, and one note when it clears. A delivery failure never fails a run.

## Why This Matters

A daemon outage is reported by something that is not the daemon. The trade-off is that the doctor cannot see inside supervised services; their health arrives as daemon alerts instead.

## When to Apply

- Before adding a doctor check, ask whether the daemon can see the condition itself. If it can, the check belongs in the daemon.
- A systemd unit that should alert when it fails sets `onFailure = [ "homelab-unit-failed@%n.service" ];`.

## Related

- [sqlite-runtime-store.md](sqlite-runtime-store.md)
