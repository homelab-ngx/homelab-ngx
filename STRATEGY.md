---
name: homelab-ngx
last_updated: 2026-11-19
---

# Strategy

## Purpose

People who run a home server want two things that usually fight: the parts
that must always work (the OS, remote access, the dashboard) and a couple of
dozen services that change all the time. When they are one system, every
service update risks the whole host.

## Positioning

Core is a small, read-only image that changes rarely and knows nothing about
the services it runs. It updates atomically and falls back by itself. Services
live in stacks: a stack is a repo with a flake and a manifest, and it runs as
its own NixOS system in a systemd-nspawn container, with its own lock,
variables, private network, update and rollback. So a service update cannot
rebuild, restart or break core. Everything stays on one machine, without VMs.

Services are Nix first: a stack is built from a commit the operator chose,
not pulled as an image someone else assembled. A stack can still run a
container image when upstream ships nothing else. Storage is bcachefs.

It is for the job Unraid and TrueNAS SCALE do: one machine for storage and
services, run from a browser. It has no network shares and no catalogue yet.
A stack is read and pinned as code, whether it is installed by URL or, later,
from a catalogue.

## Users

**Primary:** one operator who runs a home server and manages it from a
browser. They want the host to stay dependable while they add, update and
remove services as often as they like, without editing the host's
configuration.

## Boundaries

- No virtual machines for service isolation.
- No container runtime on the host; image-based apps run inside a stack.
- No second runtime daemon such as Incus in core: it would own state the
  daemon should own.
- No packages installed on the host by the operator: the image is read-only,
  and what a service needs comes with its stack.
- Nothing is left behind when something is removed: no orphaned secrets,
  fixtures, comments or docs.

_Resist a change when:_ it makes core's build depend on a service, or lets a
service update change core.

## Key metrics

- **Core changes caused by a stack update.** Target zero: core unit start
  times are the same before and after a stack update.
- **Stacks disturbed by a core update.** Stack units restarted when only core
  is updated.
- **Failed updates rolled back unattended.** The share of unhealthy stack,
  core and image updates returned to the version before without the
  operator.
- **Leftovers after a removal.** References to removed code in the repo;
  target zero; checked by search at review.

## Tracks

### Atomic core

The image (mkosi, Arch, A/B root, dm-verity, boot counting) and the core
extension, updated with systemd-sysupdate. Host settings (domain, network,
VPN, tunnel routes, firewall) are kept by the daemon and applied at run time,
so nothing personal is ever built into the image.

### Install and releases

The image is also the installer: written to a USB stick, it boots, prints an
address, and setup finishes from another device. Releases are semver tags
built, checked and signed in CI, and hosts check for them daily.

### Stacks anyone can install

A stack's manifest (version 9) declares the variables it needs (env, secret,
path or address), and the operator fills them in on the host. Route labels are
served under the host's domain, and a path's owner and group take the host's
ids. So a stack repo names no secret, host directory, hostname, LAN address or
uid, and anyone can install it.

## Later

What is not built is in the README's Roadmap.
