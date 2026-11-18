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
  - Setting or rotating a secret
  - Handling CLI errors
tags: [cli, json, secrets, automation, problem-json, git]
---

# Driving the homelab CLI from scripts and agents

## Context

`homelab` talks to the daemon over `/run/homelab-daemon/daemon.sock` and is never exposed over TCP. That socket needs no sign-in; the dashboard's API goes through `web.sock`, which does (`architecture-patterns/dashboard-sign-in.md`). `homelab COMMAND --help` documents each command. This doc covers the behavior a script depends on that `--help` does not explain.

## Guidance

- Put `--json` before the command: `homelab --json services status`. Results go to stdout; prompts, warnings, and errors go to stderr.
- Branch on the exit code, then on `code`. With `--json`, an error is one JSON object on stderr with `success`, `error`, and, when the daemon answered with problem+json, `status`, `code`, and `detail`. Never parse the `error` sentence. If the daemon is down, the CLI prints `contacting daemon: ...` and exits non-zero.
- A warning is not a failure. In particular, a secret can be committed locally while its push is still pending.
- `homelab deploy` deploys core from main through the daemon and waits. Exit code 0 means `healthy`; with `--json` the deploy record is on stdout, and `result` says `rolled_back`, `build_failed`, `failed` or `interrupted` otherwise.

## Why This Matters

Secret mutations have git side effects:

- The daemon encrypts `nixos/secrets/secrets.yaml` and commits it on the **currently checked-out branch**, never on `main`. This keeps a new secret in the same commit history as the feature that declares it.
- It then runs `git push --set-upstream origin HEAD`. If the push fails, the response still succeeds, with `push_pending: true`, a `warnings` array, the commit hash, and the branch, so a script can retry the push separately.
- A mutation is rejected before encryption when HEAD is detached or `secrets.yaml` already has uncommitted changes.

## Examples

Never pass a secret value as an argument. Pipe it:

```bash
printf '%s' "$SECRET_VALUE" | sudo homelab secret set service/KEY --value-stdin
printf '%s' "$SECRET_VALUE" | sudo homelab secret add service/KEY --description "Purpose" --value-stdin
```

`--value-stdin` strips at most one trailing LF or CRLF, keeps every other byte, and works without a TTY.

## Related

- [code-first-huma-api-problem-json.md](../architecture-patterns/code-first-huma-api-problem-json.md)
