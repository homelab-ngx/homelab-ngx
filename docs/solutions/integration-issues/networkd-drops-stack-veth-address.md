---
title: networkd takes the stacks' veths and drops the daemon's address
date: 2026-11-20
category: integration-issues
module: apps/core/internal/stacks, apps/core/internal/hostcfg
problem_type: integration_issue
component: stacks
symptoms:
  - A running stack is no longer served; its start ends in "the stack is started but not healthy" with a timeout to 10.99.N.2
  - ve-<stack> on the host has a 192.168.x.y/28 address and no 10.99.N.1
  - networkd logs "ve-<stack>: Reconfiguring with /usr/lib/systemd/network/80-container-ve.network"
root_cause: two_owners_of_one_link
resolution_type: code_change
severity: high
tags: [systemd-networkd, nspawn, veth, stacks, confext, forwarding]
---

# networkd takes the stacks' veths and drops the daemon's address

## Problem
The daemon gives the host end of each stack's veth (`ve-<stack>`) the
address 10.99.N.1 with `ip address replace`. systemd ships
`80-container-ve.network`, which matches every `ve-*` veth, and networkd
configures the link with it as soon as nspawn creates it. So the link had two
owners. Whenever networkd configured the link again, it removed the
address it had not set itself. The stack then had no way to the host, and
Caddy had no route to it.

networkd configures a link again on `networkctl reconfigure`, on a restart
of networkd, and on a `networkctl reload` that follows a change in /etc.
The last one is the trap. The host settings are a confext, and each change
ends with `systemd-confext refresh` and `networkctl reload`. The first such
reload after a boot reconfigures every link: `enp0s2`, `wg-homelab` and every
`ve-*`. Later reloads in the same boot reconfigure only the links whose files
changed. This was seen on systemd 262.2: a reboot, a running stack, one
`systemd-confext refresh --always-refresh=yes && networkctl reload`, and
10.99.0.1 was gone. On the same boot a second refresh left it in place.

So the first host settings change after a boot cut off every running stack.
That was already the case in 0.3.1. It went unseen because a fresh install
makes its first settings changes before any stack runs.

## Fix
One owner. The daemon writes `/run/systemd/network/10-homelab-stacks.network`
(`[Match] Kind=veth Name=ve-*`, `[Link] Unmanaged=yes`). Its name sorts ahead
of the stock file, so networkd leaves `ve-*` alone. The daemon writes it at
each start before it starts or reconnects a stack, and reloads networkd only
when the file was missing or different (`stacks/network.go`, `ownLinks`).
It is in /run, so a host that updates with stacks running gets it at the
daemon's start. When the link turns unmanaged, networkd drops its own
addresses and keeps 10.99.N.1. `connect` sets the address again in any case.

The stock file also turned on IPv4 and IPv6 forwarding globally, through
`IPMasquerade=both`. A stack needs IPv4 forwarding to reach the internet
through the masquerade in its own nftables table, so `linkUp` now turns on
`net.ipv4.ip_forward`. IPv6 forwarding is no longer turned on, because a
stack has no IPv6.

Two other ways were set aside:
- A per-stack `.network` file with networkd owning the address: networkd
  would have to be reloaded at every stack start and stop, and the
  container's boot would wait on networkd instead of on the daemon.
- A `ve-*` file that keeps foreign configuration: `KeepConfiguration=` is
  about networkd's start and stop, not about a reconfigure, so it would not
  stop the drop.

## What changes on a host
With a stack running, IPv6 forwarding used to be on for the whole host, a
side effect of the stock file's `IPMasquerade=both`. It is off now (checked
on a VM: `net.ipv6.conf.all.forwarding` is 0 with a stack running).
systemd-networkd accepts router advertisements on a link unless IPv6
forwarding is on, so on a LAN that sends them a host running stacks now
gets an IPv6 address and default route, as a host without stacks always
did. The host firewall is an `inet` table and covers both. The check's VM
gets no router advertisements, so the new address itself was not seen there.

## How it is checked
`TestStackLinksAreTheDaemons` (`stacks/runtime_test.go`) covers the file,
the reload, and forwarding. The image check's `stackrace` (step 10) runs
`networkctl reload` and `networkctl reconfigure ve-site` against a running
stack, and reloads networkd during a stack's start. The address and the
page must both survive.
