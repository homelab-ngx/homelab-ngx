---
name: homelab
description: Operate the stacks on this homelab-ngx host and read their logs - list, start, stop, update or roll back a stack, set its variables and folders, restart one of its services, check backups and host health, and find why a stack or service fails. Also for a new service: find a public stack or write one in a local repo. Use for any question about a service, stack, backup or log on this machine.
---

# Stacks on homelab-ngx

A stack is a group of services built from its own git repo: a flake and a
manifest, `homelab-stack.json`. It runs as a NixOS system in a
systemd-nspawn container named after the stack. The daemon installs, builds,
starts and updates it. The host is never rebuilt for a stack.

Every command below runs on the host as root. Put `--json` first for output
you parse. A command that fails exits non-zero and prints the daemon's error.

## Look

```sh
homelab --json stacks list      # each stack: running, update_state, approved and fetched commit
homelab stacks show <stack>     # generations, the last operation and its error, what the manifest asks for
homelab services status         # every service of every stack, as <stack>-<unit>, with its state
homelab doctor                  # host checks: daemon, caddy, disk, failed units, SMART, tunnel
homelab backup status           # each backup's last and next run
```

`update_state` is `up_to_date`, `commits_available` (an approved commit is
not running yet) or `needs_approval` (the fetched commit asks for new
grants).

## Change

| To | Run | Note |
|---|---|---|
| Start, stop | `homelab stacks start <stack>`, `stop <stack>` | Follows the operation to its end. The first start builds the stack. |
| Restart one service | `homelab services restart <stack>-<unit>` | Also `start` and `stop`. The stack must be running. |
| Get the newest commit | `homelab stacks fetch <stack>` | `homelab stacks check` fetches every stack. |
| Approve a commit | `homelab stacks approve <stack> <rev>` | Approves every grant it asks for. Ask the user first. |
| Update | `homelab stacks update <stack>` | Builds the approved commit and switches to it. An unhealthy result is rolled back. |
| Go back | `homelab stacks rollback <stack> [--generation N]` | The generation before by default. |
| Variables | `homelab stacks var list <stack>`; `var set <stack> <NAME> --value-stdin`; `var rm` | Secret values are never shown. A service sees a new value at its next restart. |
| Folders | `homelab stacks folder list <stack>`; `folder add <stack> <host-folder> <mount> [--read-write]` | The stack sees a change at its next restart. |
| Freeze, thaw | `homelab stacks freeze <stack>`, `thaw <stack>` | A frozen stack refuses start, update, rollback and service restarts until it is thawed. |
| Start at boot | `homelab stacks autostart <stack> on` or `off` | |
| Follow a moved repo | `homelab stacks source <stack> <source>` | What runs, the variables and the data stay. The commit found is approved by itself when it asks for nothing new. |

Ask before you stop, remove or roll back a stack, approve a commit or
change a variable. These change what the user's services do.

`homelab stacks remove` forgets a stopped stack's generations and
approvals. Its data in `/var/lib/homelab/appdata/stacks/<stack>` stays.

A stack's NixOS config is in its repo, not on this host. To change it,
change the repo (or its draft, for a stack written in the dashboard), then
`fetch` and `update`. Editing files in the stack's root does not last.

## A new service

To add a service, read [new-stack.md](new-stack.md): find a public stack
and install it, or write a new one in `~/stacks/stack-<name>`, build it and
install it from there.

## Inside a stack

The container is a machine named after the stack.

- **Its services:** `systemctl -M <stack> list-units --failed`;
  `systemctl -M <stack> status <unit>`.
- **Run a command in it:** `systemd-run -M <stack> --wait --pipe -- /run/current-system/sw/bin/<cmd> <args>`.
  `systemd-run` expands a `$VAR` itself, before the shell in the stack sees
  it.
- **On the host:** the container is `homelab-stack-<stack>.service`.
- **Unit names:** `homelab services status` calls a unit `<stack>-<unit>`.
  Inside the container it is plain `<unit>`. A unit whose name starts with
  the stack's name keeps it: `immich-server` in the stack `immich`.

## Logs

Read [logs.md](logs.md) before you analyse logs. It says where every log
is, also for a stopped stack, and the order to look in.
