---
title: A stack shell comes from machined, on the operator API only
date: 2026-11-23
category: architecture-patterns
module: apps/core
problem_type: architecture_pattern
component: stacks
severity: medium
applies_when:
  - Changing homelab stacks shell or GET /api/stacks/{name}/shell
  - Adding another way into a running container
tags: [shell, terminal, websocket, machined, nspawn, pty]
---

# A stack shell comes from machined, on the operator API only

## Context

A stack shell is root's login shell inside a running stack's container. The
CLI and the dashboard both open one. The terminal has to come from
somewhere, and the endpoint runs in the root daemon.

## Guidance

- **The terminal is machined's.** `homelab stacks shell` runs
  `machinectl shell root@<stack>`. The daemon calls the same D-Bus method,
  `org.freedesktop.machine1.Manager.OpenMachineShell`, with an empty path,
  so machined runs root's own login shell, and keeps the master end of the
  pseudo-terminal it returns. The daemon opens no PTY of its own and needs
  no PTY library. A resize is `TIOCSWINSZ` on that master end, and closing it
  hangs the shell up.
- **The fd is non-blocking.** The daemon sets `O_NONBLOCK` before it wraps
  the fd in an `os.File`, so a `Close` from the WebSocket side ends a `Read`
  waiting in the other goroutine. Linux answers `EIO` on the master once the
  shell has gone, and that is read as the end of the session.
- **Operator only.** The WebSocket is a plain handler on the operator mux
  (`cmd/daemon/server.go`), beside the stack editor's language server. That
  serves it on the CLI's socket and behind the dashboard's sign-in, and never
  on the stack listener. No API grant reaches it.
- **Refused before the upgrade.** An unknown stack is 404. A stopped one is
  409 `stack_stopped`. A frozen one is 409 `stack_frozen`, because a call
  into a frozen container waits on its bus until it times out. More than
  four open shells is 503 `shell_busy`. The browser never sees these bodies
  (a failed upgrade is close code 1006), so the shell window reads the
  stack's state to say why.

## Why this matters

Root in the container is an unprivileged uid range on the host
(`--private-users=pick`, see `privilege.md`), but the daemon that opens the
shell is root. Keeping the handler off the stack listener is what keeps a
stack's own token from opening a shell in itself or in another stack.

## Examples

```
homelab stacks shell immich
homelab stacks shell immich -- psql -U immich
```

A bare command name is taken from `/run/current-system/sw/bin`, since
machinectl wants an absolute path.
