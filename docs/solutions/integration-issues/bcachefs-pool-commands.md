---
title: What bcachefs-tools 1.39 does when a pool is made, changed and wiped
date: 2026-11-20
category: integration-issues
module: apps/core/internal/storage
problem_type: integration_issue
component: storage
symptoms:
  - A disk removed from a pool is still listed as its member
  - lsblk shows a pool's mountpoint on a disk that left the pool
  - show-super still answers on a disk after wipefs -a
  - device evacuate never returns when the other disks are too small
root_cause: tool_behaviour
resolution_type: documentation
severity: medium
tags: [bcachefs, pool, format, evacuate, wipefs, superblock]
---

# What bcachefs-tools 1.39 does when a pool is made, changed and wiped

Run on 2026-11-20 in a VM of image 0.3.941 (bcachefs-tools 1.39.7, kernel
7.3.4), on 16 GiB virtual disks. The outputs are the fixtures
`show-super-1.39.txt`, `fs-usage.txt`, `fs-usage-evacuating.txt` and
`lsblk-rota.json` in `apps/core/internal/storage/bcachefs/testdata/`.

## Format

```
bcachefs format -q -f --fs_label=pool --replicas=2 --compression=lz4 \
  --label=ssd.ssd1 /dev/vdc --label=hdd.hdd1 /dev/vdd --label=hdd.hdd2 /dev/vdb1 \
  --foreground_target=ssd --promote_target=ssd --metadata_target=ssd \
  --background_target=hdd
```

- It took 0.3 s, and the mount after it 0.2 s.
- A `--label` holds for every device after it, so each device gets its own.
- Without `-f` it asks on stdin when the device holds a file system, and
  exits 1 when stdin is closed.

## Add

`bcachefs device add --label=hdd.hdd3 <mountpoint> <device>` took 0.2 s on a
mounted pool. Like format, it needs `-f` for a device with a file system or
an old member's superblock.

`bcachefs set-fs-option --promote_target=ssd <member>` changes a target on a
mounted pool and writes it to the superblock.

## Remove

`bcachefs device evacuate <device>` sets the member to `evacuating` and
waits. The kernel moves the data.

- With the tool killed, the member stays `evacuating` and its used space
  keeps falling. Running evacuate again waits for the same move.
- `bcachefs device set-state rw <device>` stops the move. The data already
  moved stays where it is.
- Progress is the `Leaving` column of `bcachefs fs usage <mountpoint>`, in
  bytes. The tool's own progress is one line rewritten with `\r`.
- When the other disks are too small it does not fail. It prints `no
  progress for 36s` and keeps waiting, with the other disk at 96 %. The
  caller has to notice that `Leaving` stopped falling.
- A pool reads data from any copy. Slowing one disk did not slow the move
  while a copy sat on a faster disk.

`bcachefs device remove <device>` took 0.1 s after an evacuation. Run on a
member that still held data, in a pool with two copies, it also exited 0
and the pool reported no degraded data. The daemon does not rely on that
and evacuates first.

## A removed disk still looks like a member

`device remove` leaves the removed disk's own superblock. `show-super` on it
answers with the pool's UUID and label, and `lsblk` shows it as `bcachefs`.
A mount that names it with the pool's other disks succeeds and ignores it.
The daemon finds a pool's members by that UUID, so it wipes a disk after
removing it.

## A pool's mount keeps its first disk's number

The mount of a pool has the device number of the first disk it was mounted
from, and keeps it when that disk is removed. `lsblk` matches mounts by that
number: it shows the pool's mountpoint on that one disk only, and still
shows it there after the disk has left the pool and been wiped. The source
in `/proc/self/mountinfo` is right: it lists the members
(`/dev/vdf:/dev/vdg`) and changes when one is added or removed. The daemon
reads the source to tell which pool is mounted where, and whether a disk
with a mountpoint in `lsblk` is in use.

## Wipe

A member has three superblocks, at sectors 8 and 2056 and one 1 MiB before
the end (`show-super -l`). `wipefs -a` erases the first and the last.
`show-super` then prints `successful read from backup` and exits 0.

After `wipefs -a` and zeroing the first 4 MiB, `show-super` fails with
`invalid_sb_layout` and `lsblk` shows no file system. On a disk with a GPT,
`wipefs -a` on each partition and then the disk, and `blockdev --rereadpt`,
leave a disk with no partitions.

## A missing disk

`mount -t bcachefs` with one member left out fails with
`insufficient_devices_data_intact`, and names `-o degraded`. A pool with a
disk that is physically gone was not tried.

## Not tried

- Disks that spin. Every virtual disk reports `rota: true`, also an SSD's
  image file.
- A pool made by older tools. 1.39.7 refuses to evacuate one that was not
  upgraded to the `reconcile` version (read in its source, not run).
