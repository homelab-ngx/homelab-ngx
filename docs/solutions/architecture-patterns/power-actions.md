---
title: How the host reboots and powers off
date: 2026-11-20
category: architecture-patterns
module: apps/core/internal/power
problem_type: architecture_pattern
component: service_layer
severity: high
applies_when:
  - Changing how the host reboots or powers off
  - Changing how stacks stop or how pools are mounted
  - Debugging a pool that was not clean after a reboot, or a slow shutdown
tags: [power, reboot, poweroff, logind, inhibitor, stacks, pools, systemd]
---

# How the host reboots and powers off

## Context

Stacks run as transient units, `homelab-stack-<name>.service`, and bind
paths on the pools. The daemon mounts the pools and partitions itself with
mount(8) under `/var/mnt`; there are no mount units or fstab lines for them.
A plain `systemctl reboot` used to stop the containers and unmount
`/var/mnt/*` in one systemd transaction with no order between them, so a
pool could be unmounted while a container still wrote to it.

## Guidance

- **The daemon holds a logind delay lock** (`internal/power/logind.go`:
  `Inhibit("shutdown", …, "delay")`). logind sends
  `PrepareForShutdownWithMetadata(true)` before any shutdown it runs and
  waits for the lock. That covers `systemctl reboot` (systemctl asks logind
  unless `--force`), `shutdown -r HH:MM`, the power button and the daemon's
  own `POST /system/power`. The daemon then:
  1. closes the stacks to change (`BeginShutdown`): no operation begins and
     no container boots, so the monitor restarts nothing;
  2. halts the running stacks in parallel (`systemctl stop` on each unit,
     then `machinectl terminate` on a container that outlived it). A frozen
     stack is thawed first, since frozen it cannot answer the stop signal,
     unless the request asked to kill it (`systemctl kill --signal=SIGKILL`
     on its scope);
  3. syncs and unmounts what is mounted under `/var/mnt`, deepest first,
     trying a busy mount 3 times 2 s apart;
  4. writes `power_last` to the database;
  5. waits for a page to read the status as `going_down`, then closes the
     lock (see the next point).

  On an installed image, `systemctl reboot` logs "The system will reboot
  now!", then the stack unit and `var-mnt-*.mount` stop, then logind logs
  "System is rebooting." `systemd-inhibit --list` shows the lock as
  `homelab-daemon … shutdown … delay`.
- **Nothing is recorded.** An unmount clears `auto_mount`; a shutdown uses
  `Halt` and `Release`, which record nothing. The next boot mounts the same
  pools. Which stacks start is each stack's `autostart`: at the daemon's
  first start in a boot (the boot id differs from `stacks_boot_id`) every
  stack's `want_running` is set to it.
- **The wait has a limit.** The image sets `InhibitDelayMaxSec=5min`
  (`/usr/lib/systemd/logind.conf.d/10-homelab.conf`). logind's default is
  5 s, too short for one stack to halt. A daemon that hangs delays a
  shutdown by 5 minutes at most.
- **The host waits for the power page to see it ready.** The page reads
  `GET /system/power` once a second. Once the lock closes, systemd stops
  Caddy within about half a second. Without a wait, the page kept the last
  step it read, such as a pool still unmounting. So when a read came in
  during the last 3 s, the sequence waits after the last unmount until a
  read returns `going_down`, for 3 s at most. With no page open, there is
  no wait. A page that saw `going_down` shows every milestone done. One
  that did not marks the steps it saw unfinished as "not seen".
- **`POST /system/power` runs the sequence before it asks logind**, so the
  steps run even when the lock could not be taken. When logind then signals,
  the lock goes at once. If logind refuses the call, the daemon runs
  `systemctl reboot` or `poweroff`. It refuses (409 `busy`) while a stack
  operation, an update or a backup runs, unless `force`. It refuses (409
  `stacks_frozen`) while a stack is frozen, unless `thaw` (it is thawed and
  stops cleanly) or `force` (it is killed and loses what it held). A
  shutdown logind starts on its own is never refused, and thaws.
- **systemd orders the same steps when the daemon is not there.** Each stack
  unit has `TimeoutStopSec=120` and `WantsMountsFor=` on every host path it
  binds. Since systemd stops units in the reverse of their start order, the
  pool's mount unit stops after the container (`systemctl show -p After`
  lists `var-mnt-<name>.mount`). `Wants`, not `Requires`:
  unmounting a pool by hand must not stop the stack.
- **The unit properties restart no stack.** Whether a running stack needs a
  restart compares the boot arguments from `systemd-nspawn` on
  (`sameBoot`), so a stack booted before these properties existed is not
  marked for a restart.
- **A shutdown that is called off** (`PrepareForShutdownWithMetadata(false)`)
  mounts the pools again, lets the stacks start and takes the lock again.
- **The countdown is measured.** At the next start the daemon saves the time
  from `power_last` to its own start as `power_reboot_seconds`; a reboot that
  took more than an hour is not counted. `GET /system/power` reports it as
  `expected_seconds` (120 before one was measured) with this boot's
  `boot_id`. The dashboard's power page counts down from it and knows the
  host is back when `boot_id` changes.

## Limits

- `systemctl reboot --force`, `reboot -f` and `systemctl start reboot.target`
  bypass logind and the lock. Only the systemd ordering applies.
- A mount still busy after 3 tries is left to systemd's final unmount, and
  the step says so.
- A container killed after 120 s shows as `killed`; its services did not
  stop cleanly. So does a frozen stack killed without a thaw.
- A reboot after an image install thaws frozen stacks without asking.

## Related

- [stack-contract.md](stack-contract.md): how a stack stops, its scope, its firewall.
- [../integration-issues/mkosi-sysupdate-sysext-image.md](../integration-issues/mkosi-sysupdate-sysext-image.md): the reboot after an install.
