# homelab-ngx

A home server OS. Core is a small, read-only image that updates itself
atomically; every service runs in a **stack**, its own NixOS system in a
systemd-nspawn container, installed from a git repo and updated or rolled back
on its own. A dashboard and a CLI manage both.

> **Status: pre-release.** There are no published releases yet. You can build
> the image from this repo and install it, but expect breaking changes.

## What you get

- **An atomic host.** An Arch Linux image built with mkosi, with two root slots,
  dm-verity, and boot counting. It is updated whole with systemd-sysupdate, and
  a broken update falls back to the slot before by itself.
- **Core as one extension.** The daemon, the `homelab` CLI, the dashboard and
  Caddy ship as one system extension (`homelab-core`), so a core update needs
  no reboot and leaves stacks running.
- **Stacks.** A stack is a repo with a flake and a manifest
  (`homelab-stack.json`). The manifest asks for what the stack needs (routes,
  storage paths, the GPU, egress, ports, the daemon's API, variables and
  secrets), and you approve each request on the dashboard. A stack update never
  touches core.
- **Storage, backups and notifications.** An optional bcachefs pool. Named
  backups to S3, rclone or restic destinations, run by a sandboxed
  runner. Alerts through ntfy or a JSON webhook.
- **Remote access from the dashboard.** A domain with a wildcard certificate
  (DNS-01), a WireGuard VPN for your devices, a Cloudflare tunnel for the names
  you publish, Tailscale.

## Requirements

- x86_64 with UEFI, and Secure Boot turned off in the firmware: the kernel
  is not signed.
- A disk of 64 GB or more, either blank or with that much free space next to
  another OS. The system disk can be NVMe, SATA, a USB disk, eMMC, or sit
  behind a RAID or SAS controller (LSI/Broadcom, HPE Smart Array, Adaptec,
  Areca, 3ware); in a VM, a virtio, VMware, Hyper-V or Xen disk.
- 6 GB of RAM or more if the host builds its own updates; less is enough when
  it installs published releases.
- A wired network with DHCP, at least for the install: the installer has no
  Wi-Fi and no setup at the console. The host is headless and is set up and
  managed from a browser on another device.

Graphics firmware is in for Intel and AMD, network firmware for Intel,
Realtek and Broadcom cards; Mellanox ConnectX and Aquantia cards need none.
Not in: Wi-Fi, Nvidia, and Fibre Channel cards.

What is tested is what `image/check.sh` boots in a VM: virtio, USB (plain
and UAS), VMware's paravirtual SCSI and a MegaRAID controller. The other drivers are in the
image and have not been run by this project.

## Install

1. Download a release's installer, `homelab-ngx_VERSION.img`, once there is
   one, or build the image (below), which makes
   `image/out/homelab_VERSION.raw`.
2. Write that file to a USB stick of 8 GB or more and boot from it.
3. Open the address the console prints, from another device on the same
   network. Pick a blank disk or a free region, set the host name, time zone,
   SSH keys and network, and install.
   With a screen and keyboard on the host, or its first serial port, the
   stick also gives you a root shell there, without a password, to look
   around before installing. The installed host asks for a login.
4. Restart from the page; the stick can stay in until the host is up. Or
   turn the host off there, remove the stick and start it. The host answers
   at `<hostname>.local`. The first visit to the dashboard sets its password, so make it soon after the
   install. Then set up the rest in the dashboard: the domain, a storage pool,
   notifications, remote access and stacks. A forgotten password is removed
   on the host with `homelab auth reset`.

## Build

Builds need Nix with flakes, and sudo for the image build root.

```bash
make verify        # code generation drift, Go and dashboard tests, nix checks
make image         # the image and the core extension into image/out
make image-check   # boot the built image in a VM: a pool, a stack, an install
```

`make` lists every target. Each target enters the Nix dev shell by itself.

## Layout

```
apps/
  core/           # Go: the daemon (cmd/daemon), the `homelab` CLI (cmd/cli), api/ (emitted openapi.json), db/ (SQLite)
  backup-runner/  # the program a backup run starts (standard library only)
  dashboard/      # Svelte 5 + SvelteKit SPA
image/
  base/           # the mkosi definition of the image (Arch)
  core/           # the core extension's units and Caddy snippet
nix/              # flake outputs: packages, the core extension, dev shells
docs/solutions/   # why things are shaped the way they are
```

The Go modules are `homelab-ngx.com/core` and
`homelab-ngx.com/core/backup-runner`.

## License

MIT, see [LICENSE](LICENSE).
