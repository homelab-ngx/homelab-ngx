---
title: Driving the homelab CLI from scripts and agents
date: 2026-10-15
category: conventions
module: apps/core/cmd/cli
problem_type: convention
component: tooling
severity: medium
applies_when:
  - Scripting the homelab CLI or calling it from an agent
  - Setting a secret stack variable
  - Handling CLI errors
tags: [cli, json, secrets, automation, problem-json]
---

# Driving the homelab CLI from scripts and agents

## Context

`homelab` talks to the daemon over `/run/homelab-daemon/daemon.sock` and is never exposed over TCP. That socket needs no sign-in; the dashboard's API goes through `web.sock`, which does (`architecture-patterns/dashboard-sign-in.md`). `homelab COMMAND --help` documents each command. This doc covers the behavior a script depends on that `--help` does not explain.

## Guidance

- Put `--json` before the command: `homelab --json services status`. Results go to stdout; prompts, warnings, and errors go to stderr.
- Branch on the exit code, then on `code`. With `--json`, an error is one JSON object on stderr with `success`, `error`, and, when the daemon answered with problem+json, `status`, `code`, and `detail`. Never parse the `error` sentence. If the daemon is down, the CLI prints `contacting daemon: ...` and exits non-zero.
- `homelab update download`, `build`, `core` and `image` start an update through the daemon and wait. Exit code 0 means `healthy`; with `--json` the update record is on stdout, and `result` says `rolled_back`, `build_failed`, `failed` or `interrupted` otherwise. A core update restarts the daemon, so the CLI keeps asking until it answers again.
- `homelab system reboot --yes` and `homelab system poweroff --yes` (or `shutdown`) print each stack stopping and each pool unmounting, then exit 0 when the host is handed to systemd or the daemon stops answering. While a stack operation, an update or a backup runs they exit non-zero with `code` `busy`; `--force` goes ahead. `--no-wait` returns once it has started. `homelab system status` shows a shutdown under way or scheduled, and `homelab system cancel` calls off one set with `shutdown -r HH:MM`.
- `homelab system profile` shows the power profile, each tunable it wrote or the kernel refused, the last minute's idle and the devices keeping the CPU package awake. `homelab system profile set [--governor NAME|default] <power-saver|balanced|performance>` changes it at once. Both take `--json`.

## Examples

Never pass a secret value as an argument. Pipe it:

```bash
printf '%s' "$SECRET_VALUE" | sudo homelab stacks var set immich DB_PASSWORD --secret --value-stdin
```

`--value-stdin` strips at most one trailing LF or CRLF, keeps every other byte, and works without a TTY.

## Related

- [code-first-huma-api-problem-json.md](../architecture-patterns/code-first-huma-api-problem-json.md)
