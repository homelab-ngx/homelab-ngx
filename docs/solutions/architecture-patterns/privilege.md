---
title: Root manages the host; outside input runs without it
date: 2026-11-23
category: architecture-patterns
module: image, apps/core
problem_type: architecture_pattern
component: privilege
severity: high
applies_when:
  - Adding a unit, a transient unit or a helper the daemon runs
  - Handling input from the network, a stack or a repo
  - Changing SSH, the console or root's account
tags: [root, privilege, ssh, console, sandbox, users]
---

# Root manages the host; outside input runs without it

## Context

The host has one operator and no user accounts. Root is the only account that
logs in. The question for each new piece is whether it needs root, and the
answer follows one rule.

## Guidance

Root does the work of managing the host. A process that parses input from the
network, or runs code from a stack or a repo, runs without root.

**Root, and why:**

- **homelab-daemon** (`image/core/usr/lib/systemd/system/homelab-daemon.service`).
  It runs nspawn, the firewall, networking, disks, updates and backups. A
  split into an unprivileged front and a root helper would put the same
  powers behind an internal API.
- **The CLI.** `daemon.sock` is mode 0660, group `homelab-daemon`, with no
  check of its own (`apps/core/internal/httpx/serve.go`). The operator scope
  can install a stack with host folders, so a member of that group is root in
  effect. The group has no members.
- **The console.** It logs root in without asking, on the stick and on an
  installed host (`image/base/mkosi.extra/usr/lib/homelab/console-login`).
  Whoever is at the keyboard or serial port can boot anything or take the
  disk.
- **SSH.** Root logs in with a key and nothing else: `PermitRootLogin
  prohibit-password`, `AuthenticationMethods publickey`
  (`image/base/mkosi.extra/etc/ssh/sshd_config.d/10-homelab.conf`). Root's
  password field is `*` (`image/base/mkosi.finalize.chroot`). Without a key
  there is no remote shell.

There is no second login user with sudo. With `NOPASSWD` it is root behind one
more command, and with a password the host has a password to keep. Neither
stops a stolen key.

**Not root:**

- **Caddy** runs as `caddy` with `CAP_NET_BIND_SERVICE` alone and
  `ProtectSystem=strict`. It takes TLS and HTTP and passes `/api/*` to
  `web.sock`, where the dashboard's sign-in sits
  ([dashboard-sign-in.md](dashboard-sign-in.md)).
- **cloudflared** runs with `DynamicUser=yes` and its credentials through
  `LoadCredential=`.
- **Stacks** run with `--private-users=pick`: a stack's root is an
  unprivileged uid range on the host, and its binds are `:idmap`
  (`apps/core/internal/stacks/container.go`). The stack API needs the stack's
  token, its own address and a granted scope, and never gives operator scope.
- **nixd** for the stack editor drops to `homelab-nixd` with `setpriv`
  (`apps/core/internal/drafts/lsp.go`).
- **Backups** keep only `CAP_DAC_READ_SEARCH` (`apps/core/internal/backups/run.go`).
- **git and nix in an operator's own folder** run as the folder's owner. Root
  builds only the store copy, pinned by its hash
  ([stack-contract.md](stack-contract.md)).

**Nix as root.** A stack from GitHub, and a draft, are fetched and built by
root, and that is within the rule. A flake is evaluated in pure mode, which
refuses paths outside the store. Its builds run in the Nix sandbox as a
`nixbld` user (`build-users-group` in `etc/nix/nix.conf`). A flake's own
`nixConfig` is not applied: `accept-flake-config` is off, the default. A
draft's files come through the daemon, which refuses any path under `.git`.
An operator's folder differs because its `.git/config` is theirs, which root
would read.

**A new piece:** when it reads what a stranger, a stack or a repo wrote, give
it its own user (`usr/lib/sysusers.d/homelab.conf`, or `DynamicUser=`), and
start it with `NoNewPrivileges=`, `ProtectSystem=strict` and only the
capabilities it uses. When it changes the host, the daemon does it.

## Why this matters

A bug in Caddy, cloudflared or a stack's code stops at an account that
cannot change the host. A bug in the daemon reaches everything, so what
reaches the daemon is narrow: `web.sock` behind Caddy and the sign-in, and
the stack API behind a token, a source address and a granted scope.
