---
title: The image check - SSH over vsock, boots counted by QEMU, scenarios on disk snapshots
date: 2026-11-23
category: integration-issues
module: image
problem_type: integration_issue
component: testing
symptoms:
  - "no SSH after 3 minutes", with nothing in the log to say why
  - A failure in an early step hides every later step
  - Checking a fix to step 11 takes a full run of all 19 steps
root_cause: test_design
resolution_type: rewrite
severity: medium
tags: [image-check, qemu, vsock, systemd, qcow2, ovmf]
---

# The image check: SSH over vsock, boots counted by QEMU, scenarios on disk snapshots

`make image-check` was a bash script that ran 19 steps on one chain of VMs.
It took a boot as done when SSH answered on a forwarded port, and told one
boot from the next by its boot id. It is now a Go test,
`apps/core/internal/imagecheck`, built on the two choices below.

## A boot is up when its SSH answers, on a boot QEMU counted

QEMU gives each VM a `vhost-vsock-pci` device with a guest CID of its own,
and a QMP socket. The check counts boots by QMP's `RESET` events, so it knows
which boot answers: one that began after the boot it waits to replace. That
replaces comparing boot ids and the waits that came with it. Step 11 checks
that the host does not restart after a failed boot check by waiting 30 s for
a `RESET` that must not come.

systemd-ssh-generator gives the guest's sshd a socket on vsock port 22
(`sshd-vsock.socket`), and the check's SSH goes there. The forwarded TCP
ports stay for the dashboard over HTTP (steps 1 and 4), for the SSH key set
in the installer (step 4) and for a VM kept with `KEEP=1`. The key has to be
tried over TCP: the sshd systemd starts on vsock reads root's keys from its
credential and `~/.ssh/authorized_keys` only.

The check also passes the credential `vmm.notify_socket=vsock:2:<port>` and
listens there (an `AF_VSOCK` `SOCK_SEQPACKET` socket; systemd opens one
connection per message). systemd's `READY=1` only wakes the wait, for two
reasons found in the first runs:

- The initrd sends `READY=1` too. A boot of a broken image fails in the
  initrd, sends it, and resets 5 s later.
- Release 0.3.2's installed host reloads systemd three times during its
  start-up and sends no `READY=1` after the last reload, although the journal
  says "Startup finished".

QEMU's log for each VM (`qemu-NN.log`) has the messages, the QMP events and
the boot each SSH answer came from.

The host needs `/dev/vhost-vsock` open to the user: the `vhost_vsock`
module and a udev rule giving the device mode 0666
(`.github/actions/image-env`). Without it the check refuses to start, unless
`NOVSOCK=1`, which reaches sshd on the forwarded port.

## Scenarios start from snapshots

A scenario's disks are qcow2 files. One that starts from another's
snapshot gets overlays whose backing files are that snapshot's disks. A
snapshot is saved only with the VM off: its files are made read-only and
`meta.json` records the state the later steps need (the pool's path, the
installed disk's machine ID and so on).

The snapshot's key hashes the image files of the versions in use and the
scenarios that made it. `RESUME=1` uses a snapshot whose key matches. A
snapshot made by other check code is used with a warning, since resuming
after a fix to the check is what `RESUME` is for.

The firmware's variables (`vars.fd`) go with the snapshot. Step 13 boots the
free-space install by the boot entry it registered in step 2. The scenario
`under` (steps 14 to 19) is a second machine and starts with fresh
variables.

A raw disk is still needed where a host tool writes the partition table:
`sfdisk` for the stand-in Windows disks, and step 19, which converts the
system disk to raw before it adds a partition. Reading a table works on
either format: `fixture.ReadGPT` reads raw files directly and qcow2 through
`qemu-img dd`, whose `count=` is the last block, not the number of blocks.
