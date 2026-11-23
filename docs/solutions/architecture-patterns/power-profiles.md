---
title: How the power profile is applied
date: 2026-11-23
category: architecture-patterns
module: apps/core/internal/powerprofile
problem_type: architecture_pattern
component: service_layer
severity: medium
applies_when:
  - Changing what a power profile writes
  - Debugging a host that does not reach deep package C-states
  - Debugging a device that drops out under Power saver
tags: [power, cpufreq, governor, epp, aspm, runtime-pm, usb, rapl, idle]
---

# How the power profile is applied

## Context

The image ships no power tuning. Out of the box a host keeps the kernel's
defaults: the BIOS's ASPM policy, runtime PM left `on` for many PCI devices,
USB autosuspend off for some devices, and the NMI watchdog on. One device
with ASPM L1 off is enough to keep the CPU package out of PC6 and deeper. A
desktop board with a Realtek RTL8125 NIC is a common case: r8169 turns its
ASPM off.

## Guidance

- **The daemon writes sysfs and /proc/sys itself** (`internal/powerprofile`).
  It does not use power-profiles-daemon, tuned or TLP. A home server has no
  battery, and a second program owning the governor and the energy
  preference would undo an override.
- **What each profile writes** is in `Paths.plan` (`apply.go`):

  | Tunable | Power saver | Balanced | Performance |
  | --- | --- | --- | --- |
  | Governor | powersave on intel_pstate and amd-pstate-epp, else schedutil | same | performance |
  | Energy preference | power | balance_performance | (set by the governor) |
  | ASPM policy | powersupersave | as booted | performance |
  | Each link's L1, L1.1, L1.2 | on | as booted | as booted |
  | PCI runtime PM | auto | auto | as booted |
  | USB autosuspend | auto | as booted | as booted |
  | SATA link power | med_power_with_dipm | med_power_with_dipm | max_performance |
  | NMI watchdog | off | off | as booted |
  | dirty_writeback_centisecs | 1500 | as booted | as booted |

  L0s is left alone. It does not gate package states, and it is a common
  source of PCIe AER errors.
- **"As booted" is the value the kernel booted with.** Before its first
  write to a file, the daemon records the file's value in
  `/run/homelab/power-profile-boot.json`. It records every file of the plan
  before writing any of them, because writing the ASPM policy changes the
  links' files. The record is on /run, so a daemon restart keeps it and a
  reboot clears it. Going from Power saver back to Balanced therefore turns
  the forced ASPM and autosuspend off again without a table of "off"
  values.
- **The profile is applied at start, on a change, and every 5 minutes.**
  /sys resets at boot. The 5-minute sweep catches a USB device plugged in
  since. It writes only the files whose value differs. A udev rule cannot
  do this: the core extension merges after udev's coldplug, and the rule
  would not know the chosen profile.
- **What the kernel refuses is reported, not failed.** A missing file is
  "unsupported" (a VM has almost none). EPERM on the ASPM policy means the
  firmware kept ASPM control (`_OSC` or the FADT). Each tunable reports how
  many of its files refused.
- **What it gave on one host** (Core i3-10105, RTL8125B NIC, one minute
  idle each, read from `pmc_core` and RAPL):

  | | Deepest package states | Package |
  | --- | --- | --- |
  | No profile | PC3 26% | 4.1 W |
  | Balanced | PC3 51% | 2.8 W |
  | Power saver | PC7 22%, PC8 5%, PC10 26% | 0.9 W |

  The NIC's L1, which r8169 turns off, is what kept the package at PC3.
  With it forced on, the NIC still moved 118 MB/s each way over a 1 Gbit
  peer, the same as with L1 off; a ping's round trip went from 0.6 to
  1.1 ms.
- **The idle figures** come from a 1-minute sampler:
  - `cpu*/cpuidle/state*/time`, summed over the CPUs;
  - `/sys/kernel/debug/pmc_core/package_cstate_show` for the package
    C-states, Intel only. Its counters are in µs, measured against
    `/proc/uptime` on a 10th-generation Core;
  - `powercap/intel-rapl:N/energy_uj` for the package's watts. AMD's rapl
    driver uses the same names. The counter wraps at
    `max_energy_range_uj`.

  The metrics sampler reads RAPL too, every 2 s, for `power_cpu` in the
  history.

## Limits

- Disks are not spun down. Whether a bcachefs pool lets its member disks
  stay down has not been tested, so the profile leaves them alone.
- The host is never suspended.
- A NIC that loses its link or logs AER errors under Power saver needs
  Balanced. The profile does not keep a per-device exception list.
- RAPL counts the CPU package only. Most of what PC7 to PC10 save is
  outside it (the chipset, the links), so a meter at the wall shows more
  of the difference than the package's watts do.
