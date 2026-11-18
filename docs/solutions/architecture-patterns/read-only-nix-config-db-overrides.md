---
title: Read-only nix config plus a small set of DB overrides
date: 2026-10-15
category: architecture-patterns
module: apps/core/internal/config
problem_type: architecture_pattern
component: service_layer
severity: high
applies_when:
  - Adding a daemon config field or a declareService option
  - Making a setting editable from the dashboard
  - Changing the format of config.json, the DB schema, or CLI output
tags: [config, nix, overrides, snapshot, config-json, versioning]
---

# Read-only nix config plus a small set of DB overrides

## Context

`services.yaml` used to have two writers: Nix (through a `merge-config` activation step) and the daemon's PATCH handlers. Every activation re-synced `depends_on` from Nix and silently overwrote dashboard edits, while other fields belonged to the dashboard. Nobody could tell which value was in force or where it came from.

## Guidance

- `config.json` is rendered at build time (on the image by the core extension, `nix/pkgs/core-extension.nix`). The daemon only reads it. `config.LoadNix` rejects unknown fields and unsupported versions, and the extension's build runs `homelab-daemon check-config` over the rendered file. The file is world-readable, so never put secrets in it.
- Nothing in `config.json` can be overridden at runtime. What the operator changes while the host runs is owned by the DB alone: storage pools, stacks and their variables, and the backups with their destinations (`backups`, `backup_destinations`). There is no table that layers a value over a nix one; the last, `backup_overrides`, went when backups left Nix.
- Storage pools belong to the DB (the `pools` table), not to Nix. Auto-mount follows the operator's last explicit mount or unmount, which Nix cannot know.
- A schema change to `config.json` raises its `version` in both `module.nix` and `config.SupportedVersion` (2 since `backups[]` went and `paths.backup_runner` came).
- A changed `config.json` restarts the daemon (its unit's `restartTriggers`), which reads `/etc` afresh; a pool write swaps in a new snapshot itself.

## Why This Matters

A dashboard edit can no longer be reverted silently, and "what is configured" has one answer per value: Nix or the database, never both. A value in `config.json` is a nix change and a deploy.

Rejected alternatives: fixing the merge rules (the shared ownership was the bug), having the daemon own all config with Nix as a seed (config stops being reviewable in git), and making every field overridable (order and dependency edits conflict with Nix assertions).

## When to Apply

- A new nix-owned config section must be added to `module.nix` (`daemonConfig`) and to `config.Nix` in the same change. If they drift, loading fails, and so does the `daemon-config-contract` check.
- Changing the format of anything already deployed needs a version step: bump `config.json`'s `version`, add a goose migration for the DB, or version the CLI output. Never reshape deployed data silently.

## Related

- [sqlite-runtime-store.md](sqlite-runtime-store.md)
