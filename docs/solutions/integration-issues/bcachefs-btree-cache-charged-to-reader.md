---
title: bcdu fills the bcachefs btree cache and the daemon gets the bill
date: 2026-11-17
category: integration-issues
module: apps/core/internal/storage, apps/core/internal/metrics
problem_type: integration_issue
component: storage
symptoms:
  - The overview showed "Core" at 6.6 GB with the daemon's own heap at 38 MiB
  - homelab-daemon.service's memory.stat shows ~1.5 GiB of vmalloc
root_cause: kernel_cache_accounting
resolution_type: code_change
severity: medium
tags: [bcachefs, bcdu, btree-cache, cgroup, memory, metrics]
---

# bcdu fills the bcachefs btree cache and the daemon gets the bill

## Problem
Every bcdu run (folder sizes, the file browser's usage) reads every node of
the pool's extents and reflink btrees. bcachefs keeps each node in its btree
cache, which it allocates with vmalloc and charges to the memory cgroup of
the process that did the read. That process is the daemon. On 2026-11-17
the cache held 1.5 GiB (extents 829M, reflink 501M), and all of it showed as
daemon memory. The kernel shrinks this cache only under memory pressure,
and MemAvailable does not count it as available, so it sat there as used
memory until the next scan.

## What the cache is worth
It caches file metadata: where file data lives, inodes and directory
entries. The working set is small. After a manual shrink the cache settled
at ~376M and did not grow back. The pool keeps metadata on the SSD
(`metadata_target=ssd`), so a miss costs a quick SSD read.

## Solution
- After every bcdu run, `bcachefs.Client.ShrinkBtreeCache` writes a large
  count to `/sys/fs/bcachefs/<uuid>/internal/trigger_btree_cache_shrink`.
  That is exactly **one** shrinker pass. Measured right after a fresh scan,
  one pass took the cache from 1.65G to 271M. A second pass took it to 76M,
  which cuts into the working set, so do not loop it.
- Background folder measuring runs weekly (`sizeInterval`). Measure on the
  Storage page and the file browser stay on demand.
- Metrics: `cgroupMemory` subtracts `vmalloc` as well as `inactive_file`,
  so cache a cgroup was charged for counts as Kernel, not as that cgroup.
  Core is split into Services (system.slice, the daemon among it, plus
  init.scope), Sessions (user.slice, shown as "Core (Claude)") and Kernel
  (the rest of the host). Rows of older layouts' parts (`@core`,
  `@daemon`) in metrics.db are skipped.

## Checking it
```
cat /sys/fs/bcachefs/*/internal/btree_cache   # live: and per-btree sizes
grep vmalloc /sys/fs/cgroup/system.slice/homelab-daemon.service/memory.stat
```
