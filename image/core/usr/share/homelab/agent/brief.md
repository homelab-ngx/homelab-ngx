# This machine runs homelab-ngx

homelab-ngx is a home server OS. You are on the host, as root. The core
(homelab-core) ships this file, so it describes the core this host runs.

## How the host is built

- `/usr` and `/etc` are read-only. The root is an image, replaced whole by
  each update. The core is a system extension merged over `/usr`.
- `/var` is writable and holds every piece of state.
- There is no package manager to use: `pacman` cannot write to `/usr`.
  `nix shell nixpkgs#<tool>` runs a tool once; `nix profile install` keeps it.
- homelab-daemon (`homelab-daemon.service`) runs everything. Caddy
  (`caddy.service`) serves the dashboard and every route.
- Services run in stacks. A stack is a NixOS system built from its own git
  repo, run in a systemd-nspawn container named after the stack. Its
  services are not units on the host.

## How to change things

Use the `homelab` CLI. It talks to the daemon over
`/run/homelab-daemon/daemon.sock`, so the dashboard sees every change it
makes. Put `--json` first for output you parse: `homelab --json stacks list`.
`homelab <command> --help` documents each command.

Don't:

- edit files under `/etc` or `/var/lib/confexts`; the daemon rewrites them
  from its settings;
- edit a stack's files under `/var/lib/homelab/appdata/stacks/<name>/root`
  to change its config; that is the stack's own system, rebuilt by an
  update. A stack is changed in its repo, and a new one is written in a
  repo of its own;
- open or write `/var/lib/homelab-daemon/homelab.db`;
- put a secret on a command line: `homelab stacks var set <stack> <NAME>
  --value-stdin` reads it from stdin;
- stop or restart `homelab-daemon`, `caddy` or `sshd` unless asked to;
- start, stop or remove a stack, or reboot, without asking first.

For stacks, their logs and new services, use the `homelab` skill. Without skills, read
`/usr/share/homelab/agent/skills/homelab/SKILL.md`.
