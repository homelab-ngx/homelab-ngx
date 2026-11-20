#!/usr/bin/env bash
# check.sh: boot the image `make image` built (image/out) in a VM and check
# it end to end. The disk image is the installer medium (an 8 GB stick):
#   1. it has the disk UUID the image knows its installer medium by, stays
#      under 1.5 GB, boots on its verity root into installer mode with root
#      logged in on the screen and the serial port, and lists the disks; a
#      second browser is refused the installer and takes it over with the
#      code the console shows
#      (a Windows disk with the 100 MB ESP Windows makes is refused, with
#      the reason)
#   2. it installs into the free region picked on a stand-in Windows disk (an
#      ESP with another system's loaders, a gap, an NTFS partition), leaves
#      the gap alone and turns off
#   3. as a USB stick it boots as the installer next to that install (same
#      version, so the same root partition UUIDs and labels), running from
#      the stick alone; it erases a blank disk and installs onto it
#   4. that disk boots alone with the core merged, the installer's host name
#      and a machine ID of its own, asking for a login; a domain set through
#      the host settings serves the dashboard over HTTPS
#   5. a bcachefs pool is formatted and mounted through the daemon, at
#      /var/mnt/pool; unmounted, its directory takes no writes. A disk with
#      another file system is offered as a partition and mounted by name
#   6. a stack (the drafts template) is built by Nix and serves a page from
#      the pool at its route under the domain. A second pool is made from
#      three more disks through the daemon, grown with a disk that held
#      ext4, shrunk by one disk with its files kept, and deleted; the pool
#      the stack uses is not
#   7. host settings: a VPN device, a tunnel route, the firewall, host name
#      and time zone take effect without a rebuild
#   8. all of it holds after a reboot
#   9. the next version is published on a release server (signed with a key
#      of the check's own): a copy whose SHA256SUMS changed after signing is
#      refused, the real one downloads and is staged; then a core update to
#      it through the API, without a reboot, the stack untouched
#  10. an image update to the downloaded version through the API, with a
#      reboot; everything holds. The version before boots once more when the
#      API is asked, and the newest again after it
#  11. a third version installs in one operation, image and core; its root
#      is corrupted on purpose, so it falls back to the one before by itself,
#      and the daemon says so. On that version, good before: a daemon that
#      cannot start leaves the host up with the boot check failed, not in a
#      restart loop; and a core extension that does not start is replaced at
#      boot by the one before, once. Before all that: a downloaded core is in
#      use after a restart, and one that names a newer image is refused
#  12. with BUILD=1: the host builds an update from main itself
#  13. the free-space install boots alone, next to the other system's files
#  14. an erased disk whose boot loader goes on another disk's ESP boots
#      with that ESP at /boot and Windows in its boot menu, the stick left in
#  15. on that boot nothing of the stick's is used, and nobody is logged in
#      on a console
#  16. the stick boots next to it on a machine without a firmware UUID,
#      installs over it, and restarts from the page into the new system with
#      the stick still in
#  17. that system has the same machine ID on its next boot; every install
#      of the run has its own. The boot has the disk behind VMware's
#      paravirtual SCSI controller (the boot loader on a virtio disk, which
#      the firmware can read)
#  18. it boots the same behind a MegaRAID and USB UAS: the initrd has the
#      storage drivers the image names besides mkosi's defaults
#  19. given a partition named nix, as a host installed before the store
#      moved to /var has, it mounts that partition at /nix
# The firmware's variables last the whole run, as on a real machine, so the
# boot entries the installs register are used. Where the stick has to start
# next to an installed disk (steps 3 and 16) the firmware is told to try it
# first, like a machine set to start from USB.
# With two versions, steps 1 to 13 are the first one's (its installer, its
# installs and the updates from it) and steps 14 on install the second from
# its own installer medium: in the workflow the first is the release before,
# and the second the build under test.
# Steps 9-10 need two versions in image/out and step 11 three. The third is
# only installed and broken, so a copy of the second does (image/variant.sh):
#   make image VERSION=0.0.1 && make image VERSION=0.0.2 && make image-variant FROM=0.0.2 VERSION=0.0.3
# Run inside the image shell (`make image-check`). Each VM disk is deleted
# after the last step that uses it, and a guest's discards free its file's
# blocks: a hosted runner has little room. A run that passes leaves the logs
# in image/out/vm; one that fails leaves the disks it had too. KEEP=1 leaves
# the VM running when a step fails. CPUS and MEM (MiB) size the VM: by
# default a small host's 4 CPUs and 4 GB, 6 GB with BUILD=1.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
out=$here/out
vm=$out/vm
mapfile -t versions < <(ls "$out"/homelab_*.efi 2>/dev/null | sed -E 's|.*/homelab_(.+)\.efi$|\1|' | sort -V)
[ "${#versions[@]}" -gt 0 ] || { echo "no image in $out: run make image first" >&2; exit 1; }
# Boot the oldest of the newest three, update to the next, break the last.
n=${#versions[@]}
version=${versions[$((n > 3 ? n - 3 : 0))]} next= broken=
[ "$n" -ge 2 ] && next=${versions[$((n > 3 ? n - 2 : 1))]}
[ "$n" -ge 3 ] && broken=${versions[-1]}
core=$out/core/homelab-core_$version.raw
# Steps 14 on install from a stick of their own and judge that version: the
# one under test where its installer medium was built, not the release
# before it, which is published and cannot be fixed.
under=$version
[ -n "$next" ] && [ -e "$out/homelab_$next.raw" ] && under=$next
[ -e "$out/homelab_$version.raw" ] && [ -e "$core" ] || { echo "image $version is incomplete in $out: run make image again" >&2; exit 1; }
domain=check.example.com

# step "N. what": a step's heading; scripts/run reads the progress line.
steps=19
step() {
  roomused
  printf '\nprogress: %d%%\n▸ %s\n' "$(((${1%%.*} - 1) * 100 / steps))" "$*"
}
# roomused: the most the VM's files have taken so far (MB, in $peak), read
# at every step and before a disk is deleted.
peak=0
roomused() {
  local now
  now=$(du -sm "$vm" | cut -f1)
  [ "$now" -le "$peak" ] || peak=$now
}
# drop NAME...: delete the disks no later step uses. The VM is off.
drop() {
  local d
  roomused
  for d in "$@"; do rm -f "$vm/$d.raw"; done
}
ok() { printf '  ✓ %s\n' "$*"; }
die() {
  printf '  ✗ %s\n' "$*" >&2
  # KEEP=1 leaves the VM running to look at: ssh -i image/out/vm/key -p PORT root@127.0.0.1
  # A marker, not just the trap: die may run in a subshell ($(op ...)).
  [ "${KEEP:-}" = 1 ] && { touch "$vm/keep"; echo "  VM kept: ssh -i $vm/key -p $sshport root@127.0.0.1" >&2; }
  exit 1
}

rm -rf "$vm"
mkdir -p "$vm"
ssh-keygen -q -t ed25519 -N '' -f "$vm/key" -C image-check
# A second key, which only the installer's SSH keys setting lets in.
ssh-keygen -q -t ed25519 -N '' -f "$vm/key2" -C image-check-installer
cp --sparse=always "$out/homelab_$version.raw" "$vm/stick.raw"
# The disk the installer erases; a build on the host (BUILD=1) needs room in /var.
qemu-img create -q -f raw "$vm/disk.raw" "$([ "${BUILD:-}" = 1 ] && echo 100G || echo 80G)"
for d in pool1 pool2; do qemu-img create -q -f raw "$vm/$d.raw" 8G; done
# Three more for the pool the daemon makes, changes and deletes (step 6).
for d in pool3 pool4 pool5; do qemu-img create -q -f raw "$vm/$d.raw" 4G; done
qemu-img create -q -f raw "$vm/part.raw" 1G
# windisk FILE [GAP] [ESP]: a stand-in for a Windows disk: an ESP (512 MB
# unless given; Windows' own setup makes 100 MB) with Windows Boot Manager
# and a fallback loader of its own, then GAP free (as Windows leaves after
# shrinking or deleting a partition), an NTFS-typed partition, and the rest
# free.
windisk() {
  local esp=${3:-512M}
  qemu-img create -q -f raw "$1" 90G
  if [ -n "${2:-}" ]; then
    printf 'label: gpt\n,%s,U\n,%s,L\n,20G,EBD0A0A2-B9E5-4433-87C0-68B6B72699C7\n' "$esp" "$2" | sfdisk -q "$1"
    sfdisk -q --delete "$1" 2
  else
    printf 'label: gpt\n,%s,U\n,20G,EBD0A0A2-B9E5-4433-87C0-68B6B72699C7\n' "$esp" | sfdisk -q "$1"
  fi
  local espstart espsize
  read -r espstart espsize < <(sfdisk -J "$1" | jq -r '.partitiontable.partitions[0] | "\(.start) \(.size)"')
  truncate -s $((espsize * 512)) "$vm/esp.img"
  mkfs.vfat -F 32 -n EFI "$vm/esp.img" >/dev/null
  echo WINDOWS-BOOTMGR >"$vm/bootmgfw.efi" && echo WINDOWS-FALLBACK >"$vm/fallback.efi"
  mmd -i "$vm/esp.img" ::/EFI ::/EFI/BOOT ::/EFI/Microsoft ::/EFI/Microsoft/Boot
  mcopy -i "$vm/esp.img" "$vm/fallback.efi" ::/EFI/BOOT/BOOTX64.EFI
  mcopy -i "$vm/esp.img" "$vm/bootmgfw.efi" ::/EFI/Microsoft/Boot/bootmgfw.efi
  dd if="$vm/esp.img" of="$1" bs=512 seek="$espstart" conv=notrunc status=none
  rm "$vm/esp.img"
}
windisk "$vm/beside.raw" 2G
# A blank disk whose boot loader goes on another Windows disk's ESP.
qemu-img create -q -f raw "$vm/sys.raw" 80G
# A disk sold as 64 GB: 64 billion bytes, 59.6 GiB.
qemu-img create -q -f raw "$vm/small.raw" 64000000000
# A Windows disk as Windows' setup makes it: the ESP is too small to share.
windisk "$vm/win100.raw" "" 100M
windisk "$vm/win2.raw"
cpus=${CPUS:-4}
# A build on the host (BUILD=1): one Go compile takes about 2.5 GB.
mem=${MEM:-$([ "${BUILD:-}" = 1 ] && echo 6144 || echo 4096)}
sshport=$(shuf -i 20000-29999 -n1)
httpport=$((sshport + 1))
# The release server step 9 runs; the VM reaches it at 10.0.2.2.
relport=$((sshport + 2))
# The VM's firmware UUID. No install may take it for its machine ID.
fwuuid=9a0c1e2f-3b4d-4e5f-8a9b-0c1d2e3f4a5b

# boot [--order] [--no-uuid] DISK...: start the VM headless with root's SSH
# key as a systemd credential and the disks: virtio disks (vda, vdb, ... in
# the order given), or, written usb:FILE, a USB stick (the only sd disk),
# which the kernel finds after the others as on a real machine. pvscsi:FILE,
# megasas:FILE and uas:FILE put a disk behind VMware's paravirtual SCSI
# controller or a MegaRAID, or in a USB enclosure that speaks UAS. The
# firmware has no driver for those, so it cannot start from such a disk.
# (QEMU's MegaRAID fails writes that force unit access, so that disk says it
# has no FUA. Its LSI SAS controller shows Linux no disk at all.)
# A discard in the guest (an install erasing a disk) frees the file's blocks.
# The firmware starts what its own boot order says, as the installs left it.
# With --order it tries the disks in the order given instead, like a machine
# set to start from USB first. Every disk is in that order: OVMF deletes the
# boot entries of a disk that is not, also the ones an install registered.
# --no-uuid leaves the machine without a firmware UUID, as some boards are.
boot() {
  local order='' uuid=(-uuid "$fwuuid") disks=() first='' n=0 d dev
  while [ $# -gt 0 ]; do
    case $1 in
      --order) order=1 ;;
      --no-uuid) uuid=() ;;
      *) break ;;
    esac
    shift
  done
  if [ ! -e "$vm/vars.fd" ]; then
    cp "$HOMELAB_OVMF/OVMF_VARS.fd" "$vm/vars.fd"
    chmod 644 "$vm/vars.fd"
  fi
  case " $*" in *" usb:"*) disks+=(-device qemu-xhci) ;; esac
  for d in "$@"; do
    dev=virtio-blk-pci
    case $d in
      usb:*) dev=usb-storage d=${d#usb:} ;;
      pvscsi:*) disks+=(-device "pvscsi,id=scsi$n") dev="scsi-hd,bus=scsi$n.0" d=${d#pvscsi:} ;;
      megasas:*) disks+=(-device "megasas-gen2,id=scsi$n") dev="scsi-hd,bus=scsi$n.0,dpofua=off" d=${d#megasas:} ;;
      uas:*) disks+=(-device qemu-xhci -device "usb-uas,id=scsi$n") dev="scsi-hd,bus=scsi$n.0" d=${d#uas:} ;;
    esac
    [ -n "$first" ] || first=$d
    if [ -z "$order" ] && [ "$dev" = virtio-blk-pci ]; then
      disks+=(-drive "if=virtio,format=raw,file=$d,cache=unsafe,discard=unmap,detect-zeroes=unmap")
    else
      disks+=(-drive "if=none,id=disk$n,format=raw,file=$d,cache=unsafe,discard=unmap,detect-zeroes=unmap" -device "$dev,drive=disk$n${order:+,bootindex=$n}")
    fi
    n=$((n + 1))
  done
  qemu-system-x86_64 -machine q35,accel=kvm -cpu host -smp "$cpus" -m "$mem" "${uuid[@]}" \
    -drive "if=pflash,format=raw,readonly=on,file=$HOMELAB_OVMF/OVMF_CODE.fd" \
    -drive "if=pflash,format=raw,file=$vm/vars.fd" "${disks[@]}" \
    -smbios "type=11,value=io.systemd.credential.binary:ssh.authorized_keys.root=$(base64 -w0 < "$vm/key.pub")" \
    -nic "user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:$sshport-:22,hostfwd=tcp:127.0.0.1:$httpport-:80" \
    -display none -serial "file:$vm/serial-$(basename "$first" .raw).log" \
    -monitor "unix:$vm/monitor.sock,server,nowait" -daemonize -pidfile "$vm/qemu.pid"
  up
}
up() {
  for _ in $(seq 1 60); do
    if vssh true 2>/dev/null; then return 0; fi
    sleep 3
  done
  die "no SSH after 3 minutes (serial log in $vm)"
}
reboot_vm() {
  vssh 'systemctl reboot' || true
  for _ in $(seq 1 30); do vssh true 2>/dev/null || break; sleep 1; done
  up
  # The daemon applies the host settings and mounts the pool after start.
  for _ in $(seq 1 30); do api http://x/api/version >/dev/null 2>&1 && break; sleep 2; done
}
# The keepalive ends a connection the VM's reboot cut: QEMU's port forward
# keeps it open, and without one it waits forever.
vssh() {
  ssh -q -i "$vm/key" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=5 -o ServerAliveInterval=5 -o ServerAliveCountMax=3 -p "$sshport" root@127.0.0.1 "$@"
}
vscp() { scp -q -i "$vm/key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -P "$sshport" "$@"; }
stop() {
  [ -n "${relpid:-}" ] && kill "$relpid" 2>/dev/null && relpid=
  [ -e "$vm/qemu.pid" ] && [ ! -e "$vm/keep" ] || return 0
  vssh poweroff 2>/dev/null || true
  for _ in $(seq 1 30); do kill -0 "$(cat "$vm/qemu.pid")" 2>/dev/null || break; sleep 1; done
  kill "$(cat "$vm/qemu.pid")" 2>/dev/null || true
  rm -f "$vm/qemu.pid"
}
trap stop EXIT
# off: wait for the VM to turn itself off.
off() {
  for _ in $(seq 1 60); do kill -0 "$(cat "$vm/qemu.pid")" 2>/dev/null || break; sleep 1; done
  kill -0 "$(cat "$vm/qemu.pid")" 2>/dev/null && die "the VM did not turn off"
  rm -f "$vm/qemu.pid"
}
api() { vssh "curl -s --fail-with-body --unix-socket /run/homelab-daemon/daemon.sock -H 'Content-Type: application/json' $*"; }
# console_users: who is logged in on a console and on which (a login
# session, not SSH), a line each: "root ttyS0" on the serial port; the
# screen's is tty1, or a pty under kmscon.
console_users() {
  vssh 'for s in $(loginctl list-sessions --no-legend | awk "{print \$1}"); do
    [ "$(loginctl show-session "$s" -p Service --value)" = login ] &&
      echo "$(loginctl show-session "$s" -p Name --value) $(loginctl show-session "$s" -p TTY --value)"; done; true'
}
# autologin: root is logged in on the screen and on the serial port, as the
# installer medium does.
autologin() {
  local users
  for _ in $(seq 1 15); do
    users=$(console_users)
    awk '$1 == "root" && $2 == "ttyS0" { serial = 1 } $1 == "root" && $2 != "ttyS0" { screen = 1 }
      END { exit !(serial && screen) }' <<<"$users" && return 0
    sleep 2
  done
  die "root is not logged in on both the screen and the serial port: $(tr '\n' ';' <<<"$users")"
}
# installer: the installer's state, once its API answers.
installer() {
  for _ in $(seq 1 30); do api http://x/api/installer >/dev/null 2>&1 && break; sleep 2; done
  api http://x/api/installer || die "the installer does not answer"
}
# stickname: the installer medium's kernel name in the VM (vda as a virtio
# disk, sda as a USB stick), found by its disk UUID.
stickname() { vssh 'lsblk -dno NAME,PTUUID' | awk -v u="$medium" 'tolower($2) == u { print $1 }'; }
# runs_from DISK [LOADER]: the running system is all DISK's: its /var, its
# /nix (a folder of /var) and both halves of its verity root (data and hash), started by the
# boot loader on LOADER (DISK unless given). Disks by kernel name. The
# initrd's two services never fail a boot; what they could not do is in the
# kernel log, and nothing may be.
runs_from() {
  local got want="loader=${2:-$1} var=$1 nix=$1 root=$1 $1"
  got=$(vssh 'dmesg | grep -E "homelab-(loader-disk|machine-id):"; true')
  [ -z "$got" ] || die "the initrd could not do its part: $got"
  got=$(vssh 'bash -s' <<'EOS'
set -eu
disk() { lsblk -ndo PKNAME "$1"; }
uuid=$(tail -c +5 /sys/firmware/efi/efivars/LoaderDevicePartUUID-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f | tr -d '\000' | tr A-F a-f)
# Every partition with the loader's UUID: more than one disk shows here.
loader=$(lsblk -lno PKNAME,PARTUUID | awk -v u="$uuid" '$2 == u { print $1 }' | tr '\n' ' ')
dm=$(basename "$(readlink -f /dev/mapper/root)")
root=$(for s in /sys/block/"$dm"/slaves/*; do disk "/dev/${s##*/}"; done | tr '\n' ' ')
var=$(disk "$(findmnt -no SOURCE -M /var | tail -1)")
nix=$(disk "$(findmnt -no SOURCE -M /nix | tail -1 | sed 's/\[.*//')")
echo "loader=${loader% } var=$var nix=$nix root=${root% }"
EOS
  ) || die "could not tell which disk the system runs from"
  [ "$got" = "$want" ] || die "the system does not run from one disk: $got (expected $want)"
}
# ownid: the installed host's machine ID (in $id) is the one kept on its
# /var, not the firmware's UUID, and no other install's of this run.
ids=''
ownid() {
  id=$(vssh 'cat /etc/machine-id')
  [[ $id =~ ^[0-9a-f]{32}$ ]] || die "the machine ID is not one: $id"
  [ "$id" = "$(vssh 'cat /var/lib/homelab/machine-id')" ] || die "the machine ID $id is not the one on /var"
  vssh 'findmnt -no SOURCE /etc/machine-id' | grep -q '\[/lib/homelab/machine-id\]$' ||
    die "/etc/machine-id is not /var's file bound over it: $(vssh 'findmnt -no SOURCE /etc/machine-id')"
  [ "$id" != "${fwuuid//-/}" ] || die "the machine ID is the firmware's UUID"
  case " $ids " in *" $id "*) die "another install has the machine ID $id too" ;; esac
  ids+=" $id"
}
# put PATH JSON: PUT a body to the daemon's API, the body over stdin.
put() { vssh "cat > /tmp/body.json" <<<"$2" && api -X PUT "http://x/api/$1" -d @/tmp/body.json >/dev/null; }
# post PATH JSON: the same with POST; prints the answer.
post() { vssh "cat > /tmp/body.json" <<<"$2" && api -X POST "http://x/api/$1" -d @/tmp/body.json; }
# code METHOD PATH [BODY]: the status an API request answers with.
code() {
  vssh "curl -s -o /dev/null -w '%{http_code}' --unix-socket /run/homelab-daemon/daemon.sock -H 'Content-Type: application/json' -X $1 'http://x/api/$2' ${3:+-d '$3'}"
}
# spare: the disks a pool can be made from, as "path:contents" words.
spare() { api http://x/api/storage | jq -r '[.unassigned[] | "\(.path):\(.contents)"] | join(" ")'; }
# pool_lifecycle: a second pool on vde, vdf and vdg, made, grown, shrunk and
# deleted through the daemon. It runs once, on the first version that can:
# a release from before the daemon made pools lists no contents for a disk.
lifecycle=
pool_lifecycle() {
  [ -z "$lifecycle" ] || return 0
  api http://x/api/storage | jq -e '.unassigned | length > 0 and (.[0] | has("contents"))' >/dev/null || return 0
  local st uuid main mainname sum op
  [ "$(spare)" = "/dev/vde: /dev/vdf: /dev/vdg:" ] || die "the spare disks are not vde, vdf and vdg, blank: $(spare)"
  [ "$(code POST storage/pools '{"name":"scratch","disks":["/dev/vde"],"copies":2}')" = 409 ] || die "two copies on one disk were not refused"
  [ "$(code POST storage/pools '{"name":"scratch","disks":["/dev/vda"],"copies":1}')" = 409 ] || die "a pool on the system disk was not refused"
  st=$(post storage/pools '{"name":"scratch","disks":["/dev/vde","/dev/vdf"],"copies":2}') || die "the pool was not made: $st"
  uuid=$(jq -r .uuid <<<"$st")
  [ "$(jq -r '"\(.state) \(.mountdir) \(.data_replicas) \(.auto_mount) \(.disks | length)"' <<<"$st")" = "mounted /var/mnt/scratch 2 true 2" ] ||
    die "the new pool is not mounted at /var/mnt/scratch with 2 copies on 2 disks: $st"
  vssh 'findmnt -no FSTYPE /var/mnt/scratch' | grep -q bcachefs || die "/var/mnt/scratch is not mounted"

  # A disk with a file system on it is erased only with the pool's name.
  vssh 'mkfs.ext4 -q -L Old /dev/vdg'
  for _ in $(seq 1 10); do [ "$(spare)" = '/dev/vdg:ext4 "Old"' ] && break; sleep 2; done
  [ "$(spare)" = '/dev/vdg:ext4 "Old"' ] || die "vdg is not offered with its contents: $(spare)"
  [ "$(code POST "storage/pools/$uuid/disks" '{"disk":"/dev/vdg"}')" = 422 ] || die "a disk with contents was added without the pool's name"
  vssh 'blkid -o value -s TYPE /dev/vdg' | grep -q ext4 || die "the refused add touched vdg"
  [ "$(code POST "storage/pools/$uuid/disks" '{"disk":"/dev/vdg","confirm":"scratch"}')" = 204 ] || die "vdg was not added"
  [ "$(api http://x/api/storage | jq -r --arg u "$uuid" '.pools[] | select(.uuid == $u) | .disks | length')" = 3 ] || die "the pool does not have 3 disks"

  # Removing a disk moves its data to the others first.
  vssh 'dd if=/dev/urandom of=/var/mnt/scratch/file bs=1M count=300 status=none && sync'
  sum=$(vssh 'sha256sum < /var/mnt/scratch/file')
  [ "$(code DELETE "storage/pools/$uuid/disks?disk=/dev/vde")" = 202 ] || die "the removal of vde did not start"
  for _ in $(seq 1 60); do
    op=$(api http://x/api/storage | jq -c --arg u "$uuid" '.pools[] | select(.uuid == $u) | .operation')
    jq -e '.finished_at' <<<"$op" >/dev/null && break
    sleep 2
  done
  [ "$(jq -r '"\(.phase) \(.error // "")"' <<<"$op")" = "done " ] || die "the removal of vde did not end well: $op"
  [ "$(api http://x/api/storage | jq -r --arg u "$uuid" '.pools[] | select(.uuid == $u) | [.disks[].path] | join(" ")')" = "/dev/vdf /dev/vdg" ] ||
    die "the pool's disks are not vdf and vdg after the removal"
  [ "$(vssh 'sha256sum < /var/mnt/scratch/file')" = "$sum" ] || die "the file changed when vde left"
  [ "$(spare)" = "/dev/vde:" ] || die "the removed vde is not a blank spare disk: $(spare)"
  vssh '! bcachefs show-super /dev/vde >/dev/null 2>&1' || die "the removed vde still has a superblock"
  [ "$(code DELETE "storage/pools/$uuid/disks?disk=/dev/vdf")" = 409 ] || die "a pool with two copies gave up its second disk"

  # A pool a stack keeps a folder on is not deleted; this one is, by name.
  st=$(api http://x/api/storage | jq -c --arg m "$pool" '.pools[] | select(.mountdir == $m)')
  main=$(jq -r .uuid <<<"$st") mainname=$(jq -r .name <<<"$st")
  [ "$(code DELETE "storage/pools/$main?confirm=$mainname")" = 409 ] || die "the pool the stack uses was not refused"
  [ "$(code DELETE "storage/pools/$uuid?confirm=$mainname")" = 422 ] || die "a delete under another pool's name was not refused"
  [ "$(code DELETE "storage/pools/$uuid?confirm=scratch")" = 204 ] || die "the pool was not deleted"
  vssh '! findmnt /var/mnt/scratch >/dev/null && ! test -e /var/mnt/scratch' || die "/var/mnt/scratch is still there"
  [ "$(spare)" = "/dev/vde: /dev/vdf: /dev/vdg:" ] || die "the deleted pool's disks are not blank spare disks: $(spare)"
  vssh '! bcachefs show-super /dev/vdf >/dev/null 2>&1' || die "a deleted pool's disk still has a superblock"
  [ "$(api http://x/api/storage | jq -r '[.pools[].mountdir] | join(" ")')" = "$pool" ] || die "the pools left are not just $pool"
  vssh "findmnt -no FSTYPE $pool" | grep -q bcachefs || die "$pool is not mounted any more"
  lifecycle=1
  ok "a pool made on vde and vdf, grown with vdg (erased by name), shrunk by vde with its file kept, and deleted; the stack's pool refused"
}
# gone: wait for the VM to stop answering, as a reboot it started itself does.
gone() { for _ in $(seq 1 60); do vssh true 2>/dev/null || break; sleep 2; done; }
# daemon: wait for the daemon's API.
daemon() { for _ in $(seq 1 ${1:-30}); do api http://x/api/version >/dev/null 2>&1 && return 0; sleep 2; done; return 1; }
# stage_image V: put version V's image files in the host's update folder, as
# a build there would.
stage_image() {
  vscp "$out/homelab_$1.efi" "$out"/homelab_"$1".root*.raw root@127.0.0.1:/var/lib/homelab/updates/
}
# release V: publish version V's update files in $vm/release as the release
# workflow lays them out, SHA256SUMS signed with the check's key.
release() {
  local d=$vm/release
  rm -rf "$d" && mkdir -p "$d/$1/core"
  ln -f "$out/homelab_$1.efi" "$out"/homelab_"$1".root*.raw "$d/$1/"
  ln -f "$out/core/homelab-core_$1.raw" "$d/$1/core/"
  (cd "$d/$1" && sha256sum homelab_"$1".* core/homelab-core_"$1".raw > SHA256SUMS)
  gpg --homedir "$vm/gnupg" --batch --yes --detach-sign -o "$d/$1/SHA256SUMS.gpg" "$d/$1/SHA256SUMS"
  echo "$1" > "$d/latest"
}
# op PATH [BODY]: start an operation and wait for its result (the daemon may
# restart on the way); prints the record.
op() {
  local started id d
  if [ -n "${2:-}" ]; then
    vssh "cat > /tmp/body.json" <<<"$2"
    started=$(api -X POST "http://x/api/$1" -d @/tmp/body.json) || die "$1 refused: $started"
  else
    started=$(api -X POST "http://x/api/$1") || die "$1 refused: $started"
  fi
  id=$(jq -r .id <<<"$started")
  for _ in $(seq 1 ${OPWAIT:-180}); do
    sleep 5
    d=$(api http://x/api/system-updates/deploys 2>/dev/null | jq -c ".[] | select(.id == $id)") || continue
    [ -n "$d" ] && [ "$(jq -r .result <<<"$d")" != running ] && { echo "$d"; return; }
  done
  die "$1 did not finish"
}
# https NAME PATH: GET https://NAME.$domain PATH through Caddy in the VM.
https() { vssh "curl -skf --resolve $1.$domain:443:127.0.0.1 https://$1.$domain$2"; }

booted() {
  vssh 'grep -q "^IMAGE_VERSION=\"\?'"$1"'\"\?$" /etc/os-release' || die "booted something other than $1"
  # The boot check waits for the daemon (up to two minutes) before it passes.
  for _ in $(seq 1 75); do vssh 'systemctl is-active -q boot-complete.target' && break; sleep 2; done
  vssh 'systemctl is-active -q boot-complete.target' || die "boot-complete.target not reached"
  vssh 'findmnt -no SOURCE / | grep -q /dev/mapper/root' || die "/ is not on the verity device"
}

# served: the dashboard's API answers through Caddy at homelab.$domain: 200,
# or 401 on a version with the sign-in. hassignin: the booted version has
# the sign-in (an older release, booted first in a release's check, has not).
apicode() { vssh "curl -sk -o /dev/null -w '%{http_code}' --resolve homelab.$domain:443:127.0.0.1 https://homelab.$domain$1"; }
served() { case "$(apicode /api/version)" in 200 | 401) ;; *) return 1 ;; esac; }
hassignin() { [ "$(apicode /api/auth)" = 200 ]; }

# signin: through Caddy the dashboard's API wants a session; the first visitor
# sets the password, a second cannot, and the CLI can remove it.
signin() {
  local u="https://homelab.$domain" r="--resolve homelab.$domain:443:127.0.0.1"
  local post="-H 'Origin: $u' -H 'Content-Type: application/json' -d '{\"password\":\"image-check-pw\"}'"
  [ "$(vssh "curl -sk $r $u/api/stacks" | jq -r .code)" = setup_required ] || die "the API answers before a password is set"
  vssh "curl -sk $r -c /tmp/jar $post $u/api/auth/setup" | jq -e .signed_in >/dev/null || die "the first password was refused"
  vssh "curl -skf $r -b /tmp/jar $u/api/stacks" >/dev/null || die "signed in, the API still refuses"
  [ "$(vssh "curl -sk $r $u/api/stacks" | jq -r .code)" = login_required ] || die "the API answers without a session"
  [ "$(vssh "curl -sk $r $post $u/api/auth/setup" | jq -r .code)" = password_set ] || die "a second visitor could set the password"
  vssh 'homelab auth status' | grep -q 'has a password' || die "the CLI does not see the password"
  vssh 'homelab auth reset' >/dev/null || die "the CLI could not remove the password"
  [ "$(vssh "curl -sk $r -b /tmp/jar $u/api/stacks" | jq -r .code)" = setup_required ] || die "a session outlived the reset"
}

# stackrefused NAME: from inside the stack's network the dashboard is refused
# and the stack's own route is not. (A probe from the host's side of the veth
# would leave through lo and be masqueraded to 127.0.0.1.)
stackrefused() {
  local leader host code
  leader=$(vssh "machinectl show $1 -p Leader --value")
  host=$(vssh "ip -4 -o addr show dev ve-$1" | awk '{print $4}' | cut -d/ -f1 | grep '^10\.99\.' | head -1)
  [ -n "$leader" ] && [ -n "$host" ] || die "no leader or host address for $1"
  probe() { vssh "nsenter -t $leader -n curl -sk -o /dev/null -w '%{http_code}' --max-time 10 --resolve $2.$domain:443:$host https://$2.$domain$3"; }
  code=$(probe "$1" homelab /api/auth)
  [ "$code" = 403 ] || die "the dashboard answered $code inside $1, not 403"
  code=$(probe "$1" "$1" /)
  [ "$code" = 200 ] || die "$1's own route answered $code inside $1, not 200"
}

# fixedids: the image's own accounts keep their ids from one version to the
# next, since files in /var are owned by number
# (usr/lib/sysusers.d/homelab.conf). False on a version from before they
# were fixed.
fixedids() {
  local got
  vssh 'grep -q "^u caddy 956 " /usr/lib/sysusers.d/homelab.conf' || return 1
  got=$(vssh 'echo "$(id -u caddy):$(id -g caddy) $(getent group homelab-daemon | cut -d: -f3) $(id -u homelab-nixd)"')
  [ "$got" = "956:956 957 955" ] || die "the image's accounts have other ids than the ones it fixes: caddy, homelab-daemon, homelab-nixd are $got"
}

# hostcheck: everything the host settings asked for is in place.
hostcheck() {
  served || die "the dashboard is not served at homelab.$domain"
  # After a boot the daemon answers before it has started the stacks again.
  sitewait=0
  until https site / 2>/dev/null | grep -q image-check; do
    [ "$sitewait" -lt 120 ] || die "the stack's route is not served at site.$domain"
    sleep 2
    sitewait=$((sitewait + 2))
  done
  [ "$(vssh 'wg show wg-homelab peers' | wc -l)" -eq 2 ] || die "the VPN does not have its two devices"
  vssh 'ip -4 -o addr show wg-homelab' | grep -q 10.10.0.1/24 || die "the VPN interface has no address"
  vssh 'grep -q "originServerName: site.'"$domain"'" /var/lib/homelab/cloudflared/config.yml' || die "the tunnel route is not in cloudflared's config"
  vssh 'systemctl is-enabled homelab-cloudflared' | grep -q runtime || die "the tunnel is not turned on"
  vssh 'nft list table inet homelab_host' | grep -q 'dport 8443 accept' || die "the firewall table is not loaded"
  [ "$(vssh cat /proc/sys/kernel/hostname)" = checkhost ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname)"
  vssh 'readlink /etc/localtime' | grep -q Europe/Stockholm || die "the time zone is not set"
  [ "$(api http://x/api/stacks/site | jq -r .running)" = true ] || die "the stack is not running"
  # Caddy's way to the daemon is web.sock, behind the sign-in. In the daemon's
  # group it could open the CLI's socket, which asks for none.
  vssh 'gid=$(getent group homelab-daemon | cut -d: -f3); pid=$(systemctl show caddy -p MainPID --value)
    ! id -nG caddy | grep -qw homelab-daemon && ! grep "^Groups:" /proc/$pid/status | grep -qw "$gid"' ||
    die "Caddy is in the homelab-daemon group"
}

# claim: take the installer (its token in $token): the first to ask gets it;
# when this boot's installer is held already, with the console's code.
claim() {
  token=$(api -X POST http://x/api/installer/claim 2>/dev/null | jq -r '.token // empty' || true)
  [ -n "$token" ] && return 0
  token=$(post installer/takeover "{\"code\":\"$(vssh 'cat /run/homelab-installer/takeover-code')\"}" | jq -r '.token // empty' || true)
  [ -n "$token" ] || die "the installer could not be claimed or taken over"
}
# installto DISK ERASE HOSTNAME [IPV4] [ESP] [REGION]: install from the
# medium through the installer's API (into the free region starting at byte
# REGION). The installer's token stays in $token.
installto() {
  local st
  claim
  local plan="{\"disk\":\"$1\",\"erase\":$2${5:+,\"esp\":\"$5\"}${6:+,\"region\":$6}}"
  [ "$(api -X POST http://x/api/installer/plan -d "'$plan'" | jq -r .ok)" = true ] ||
    die "no plan for $1: $(api -X POST http://x/api/installer/plan -d "'$plan'" | jq -r .reason)"
  api -X POST -H "'X-Installer-Token: $token'" http://x/api/installer/install \
    -d "'{\"disk\":\"$1\",\"erase\":$2,\"hostname\":\"$3\",\"timezone\":\"Europe/Stockholm\",\"ssh_keys\":[\"$(cat "$vm/key2.pub")\"]${4:+,\"ipv4\":$4}${5:+,\"esp\":\"$5\"}${6:+,\"region\":$6}}'" >/dev/null || die "install refused"
  for _ in $(seq 1 120); do
    st=$(api http://x/api/installer | jq -r .step)
    [ "$st" = installing ] || break
    sleep 5
  done
  [ "$st" = installed ] || die "install onto $1: $st: $(api http://x/api/installer | jq -r '.error + "\n" + (.log[-15:] | join("\n"))')"
  # Every install can be restarted into: the firmware has its boot entry,
  # and the installer put it first.
  [ "$(api http://x/api/installer | jq -r .can_restart)" = true ] ||
    die "install onto $1: no boot entry found: $(api http://x/api/installer | jq -r '.log[-4:] | join("; ")')"
}
# install ...: installto, then turn the machine off from the page's button.
install() {
  installto "$@"
  api -X POST -H "'X-Installer-Token: $token'" http://x/api/installer/poweroff >/dev/null || true
  off
}
# espfiles DEV: the other system's files on an ESP, as one line; read where
# the ESP is mounted, else from a mount of its own.
espfiles() {
  vssh "d=\$(findmnt -no TARGET $1 | head -1); if [ -z \"\$d\" ]; then mkdir -p /run/esp && mount -o ro $1 /run/esp && d=/run/esp; fi
    cat \$d/EFI/Microsoft/Boot/bootmgfw.efi \$d/EFI/BOOT/BOOTX64.EFI | tr '\\n' ' '; [ \$d != /run/esp ] || umount /run/esp"
}
# inuki V MODULE: version V's initrd has the kernel module (its path under
# drivers/, without .ko). The initrd's modules are an uncompressed archive
# in the UKI, so a file's path can be read there. The path from lib/modules
# on is the file's own; from kernel/ on it is also a line of modules.dep,
# which lists every module of the kernel, in the initrd or not.
inuki() { grep -aq "lib/modules/[^/]*/kernel/drivers/$2\.ko" "$out/homelab_$1.efi"; }
# partuuids FILE: the UUIDs of a disk file's /var partition (and /nix, on a
# version that gave the store its own).
partuuids() { sfdisk -J "$1" | jq -r '[.partitiontable.partitions[] | select(.name == "var" or .name == "nix") | .uuid] | sort | join(" ")'; }

step "1. the installer medium boots $version into installer mode"
# The newest build's initrd has a storage driver of every kind the image
# names besides mkosi's defaults.
newest=${versions[-1]}
for m in usb/storage/uas mmc/core/mmc_block mmc/host/sdhci-pci mmc/host/sdhci-acpi ata/ata_piix scsi/megaraid/megaraid_sas scsi/mpt3sas/mpt3sas \
  message/fusion/mptsas scsi/hpsa scsi/smartpqi/smartpqi scsi/aacraid/aacraid scsi/arcmsr/arcmsr scsi/3w-9xxx scsi/3w-sas scsi/isci/isci \
  scsi/mvsas/mvsas scsi/pm8001/pm80xx ufs/host/ufshcd-pci scsi/hv_storvsc scsi/vmw_pvscsi block/xen-blkfront; do
  inuki "$newest" "$m" || die "the initrd of $newest has no $m"
done
[ "$(stat -c %s "$out/homelab_$newest.efi")" -le 200000000 ] || die "the UKI of $newest is $(stat -c %s "$out/homelab_$newest.efi") bytes: over 200 MB"
size=$(stat -c %s "$out/homelab_$version.raw")
# 1.26 GB in November 2026; the limit catches it growing back.
[ "$size" -le 1500000000 ] || die "the installer medium is $size bytes: it should stay under 1.5 GB"
# The image tells its installer medium from an installed disk by the medium's
# disk UUID, the same in every build: the constant it carries must be this.
medium=$(sfdisk --disk-id "$out/homelab_$version.raw" | tr A-F a-f)
for f in mkosi.extra/usr/lib/udev/rules.d/61-homelab-disk.rules mkosi.initrd.conf/mkosi.extra/usr/lib/homelab/loader-disk-wait; do
  grep -v '^ *#' "$here/base/$f" | grep -qi "$medium" || die "the installer medium's disk UUID is $medium, which image/base/$f does not name"
done
boot "$vm/stick.raw" "$vm/disk.raw" "$vm/beside.raw" "$vm/small.raw" "$vm/win100.raw"
booted "$version"
vssh 'systemctl --failed --no-legend' | grep . && die "failed units" || true
[ "$(stickname)" = vda ] || die "the disk with the medium's UUID is $(stickname), not vda"
runs_from vda
st=$(installer)
jq -e '.active and (.disks[] | select(.name == "vda") | .in_use) and (.disks[] | select(.name == "vdb") | .erase.ok)
  and (.disks[] | select(.name == "vdc") | .beside.ok)' <<<"$st" >/dev/null || die "unexpected disks: $(jq -c '[.disks[] | {name, in_use, erase, beside}]' <<<"$st")"
curl -sf "http://127.0.0.1:$httpport/" | grep -q '<html' || die "the dashboard does not answer over HTTP"
autologin
# A disk sold as 64 GB takes an install, where the image's partitions fit it
# (a version before that says it is too small).
small=
if [ "$(jq -r '.min_bytes' <<<"$st")" -le 64000000000 ]; then
  jq -e '.disks[] | select(.name == "vdd") | .erase.ok' <<<"$st" >/dev/null || die "a 64 GB disk is refused: $(jq -c '.disks[] | select(.name == "vdd") | .erase' <<<"$st")"
  plan=$(post installer/plan '{"disk":"vdd","erase":true}')
  [ "$(jq -r .ok <<<"$plan")" = true ] || die "no plan for a 64 GB disk: $(jq -r .reason <<<"$plan")"
  small="; a 64 GB disk offered, with a plan that fits"
fi
# Next to a Windows with the 100 MB ESP its setup makes there is no install,
# and the page says why; the disk can still be erased. (A version before the
# reason named Windows refuses it too.)
tiny=$(jq -c '.disks[] | select(.name == "vde")' <<<"$st")
jq -e '.erase.ok and (.beside.ok | not)' <<<"$tiny" >/dev/null || die "a disk with a 100 MB ESP: $(jq -c '{erase, beside}' <<<"$tiny")"
if vssh 'test -e /usr/lib/systemd/system-generators/homelab-nix'; then
  jq -r .beside.reason <<<"$tiny" | grep -q 'Windows makes one of 100 MB' || die "the refusal does not say why: $(jq -r .beside.reason <<<"$tiny")"
  plan=$(post installer/plan '{"disk":"vde","erase":false}')
  [ "$(jq -r .ok <<<"$plan")" = false ] && jq -r .reason <<<"$plan" | grep -q '400 MB' || die "a plan next to a 100 MB ESP: $plan"
  small="$small; free space next to a 100 MB ESP refused with the reason"
fi
# The installer is one browser's, and not for ever: a second claim is
# refused, the code on the console takes it over, and the token from before
# holds nothing after that. A failed boot check never restarts the stick,
# which has no tries to count. (A version before the take-over has none of
# this.)
held=
if vssh 'test -e /run/homelab-installer/takeover-code'; then
  code=$(vssh 'cat /run/homelab-installer/takeover-code')
  [[ $code =~ ^[0-9]{6}$ ]] || die "the take-over code is not six digits: $code"
  first=$(api -X POST http://x/api/installer/claim | jq -r .token) || die "the first claim was refused"
  [ "$(api -X POST http://x/api/installer/claim | jq -r .code || true)" = installer_claimed ] || die "a second claim was not refused"
  [ "$(post installer/takeover '{"code":"x"}' | jq -r .code || true)" = invalid_install ] || die "a wrong take-over code was not refused"
  second=$(post installer/takeover "{\"code\":\"$code\"}" | jq -r .token) || die "the console's code did not take the installer over"
  [ "$(api -H "'X-Installer-Token: $second'" http://x/api/installer | jq -r .mine)" = true ] || die "the installer is not the taker's"
  [ "$(api -H "'X-Installer-Token: $first'" http://x/api/installer | jq -r .mine)" = false ] || die "the token from before the take-over still holds the installer"
  # Read whole, then searched: grep -q would close the pipe on ssh.
  greeting=$(vssh 'bash -lic true 2>/dev/null')
  grep -q "take-over code: $code" <<<"$greeting" || die "the console's greeting does not show the take-over code"
  grep -q '█' <<<"$greeting" || die "the console's greeting draws no QR code of the address"
  [ "$(vssh '/usr/lib/systemd/systemd-bless-boot status')" = clean ] || die "the stick's boot is counted: $(vssh '/usr/lib/systemd/systemd-bless-boot status')"
  [ "$(vssh 'systemctl show homelab-boot-check.service -p FailureAction --value')" = none ] || die "a failed boot check would restart the stick"
  [ "$(jq -r '.machine.memory > 0 and (.machine.secure_boot | not) and .machine.boot_entries' <<<"$st")" = true ] || die "the machine's facts: $(jq -c .machine <<<"$st")"
  held="; a second claim refused, taken over with the console's code"
fi
ok "$((size / 1000000)) MB, disk UUID $medium; verity root, boot-complete, no failed units; the medium in use, a blank disk and free space beside Windows offered; root logged in on the screen and the serial port$small$held"

step "2. install into the free space next to the other system"
# The large free region after the NTFS partition, not the 2 GB gap before it.
region=$(jq -r '.disks[] | select(.name == "vdc") | .free | max_by(.size) | .start' <<<"$st")
install vdc false beside '{"mode":"static","address":"10.0.2.15/24","gateway":"10.0.2.2","dns":["10.0.2.3"]}' "" "$region"
drop small win100
ntfs=$(sfdisk -J "$vm/beside.raw" | jq '.partitiontable.partitions[] | select(.type == "EBD0A0A2-B9E5-4433-87C0-68B6B72699C7") | .start')
ingap=$(sfdisk -J "$vm/beside.raw" | jq --argjson n "$ntfs" '[.partitiontable.partitions[] | select(.start > 2048 and .start < $n)] | length')
[ "$ingap" = 0 ] || die "$ingap new partitions went into the gap before the other system's: $(sfdisk -d "$vm/beside.raw")"
sfdisk -d "$vm/beside.raw" | grep -q homelab-reserved && die "a temporary partition was left on the disk"
ok "installed into vdc's selected free region (not the gap before the other system) and turned off"

step "3. the USB stick boots as the installer next to that install; erase a disk and install"
# The first install stays attached: its root and verity partitions have the
# stick's UUIDs, its /var the stick's label, and as virtio disks
# the kernel finds them before the stick. The firmware would start that
# install (its boot entry is first), so it is told to start the stick.
boot --order "usb:$vm/stick.raw" "$vm/disk.raw" "$vm/beside.raw"
booted "$version"
stick=$(stickname)
[ -n "$stick" ] || die "the stick is not among the VM's disks"
runs_from "$stick"
st=$(installer)
jq -e --arg s "$stick" '.active and (.disks[] | select(.name == $s) | .in_use) and (.disks[] | select(.name == "vda") | .erase.ok)
  and (.disks[] | select(.name == "vdb") | (.in_use | not) and .erase.ok)' <<<"$st" >/dev/null ||
  die "not the installer, or unexpected disks: $(jq -c '{active, disks: [.disks[] | {name, in_use, erase}]}' <<<"$st")"
autologin
# Step 13 starts the first install through its boot entry: the firmware must
# have kept it when it was told an order (see boot).
esp=$(sfdisk -J "$vm/beside.raw" | jq -r '.partitiontable.partitions[0].uuid')
vssh efibootmgr | grep -qi "GPT,$esp," || die "the firmware dropped the first install's boot entry: $(vssh efibootmgr | tr '\n' ';')"
install vda true installed
# Every install has partition UUIDs of its own, and none the medium's disk UUID.
[ "$(sfdisk --disk-id "$vm/disk.raw" | tr A-F a-f)" != "$medium" ] || die "the installed disk has the medium's disk UUID"
[ -n "$(partuuids "$vm/disk.raw")" ] && [ "$(partuuids "$vm/disk.raw")" != "$(partuuids "$vm/beside.raw")" ] ||
  die "two installs share their /var partition UUID: $(partuuids "$vm/disk.raw")"
ok "the stick ($stick) ran from itself next to an install of $version and offered that disk; installed onto vda and turned off"

step "4. the installed disk boots $version with its core, its name, machine ID, time zone, SSH key and a domain"
boot "$vm/disk.raw" "$vm/pool1.raw" "$vm/pool2.raw" "$vm/part.raw" "$vm/pool3.raw" "$vm/pool4.raw" "$vm/pool5.raw"
booted "$version"
runs_from vda
ownid
installed=$id
vssh 'test "$(findmnt -no SIZE /var | tr -d G | cut -d. -f1)" -gt 30' || die "/var does not fill the disk"
# The Nix store is on /var: no partition of its own takes room from it (a
# release from before that, installed first in a release's check, has one).
onvar=
if vssh 'test -e /usr/lib/systemd/system-generators/homelab-nix'; then
  onvar=1
  vssh 'findmnt -no SOURCE -M /nix' | grep -q '\[/nix\]$' || die "/nix is not /var's folder: $(vssh 'findmnt -no SOURCE -M /nix')"
  sfdisk -d "$vm/disk.raw" | grep -q 'name="nix"' && die "the install made a nix partition" || true
else
  vssh 'mountpoint -q /nix' || die "/nix is not mounted"
fi
vssh 'systemd-sysext status --no-legend' | grep -q homelab-core || die "the core extension is not merged"
for _ in $(seq 1 30); do api http://x/api/version >/dev/null 2>&1 && break; sleep 2; done
api http://x/api/version >/dev/null || die "the daemon does not answer"
[ "$(api http://x/api/installer | jq -r .active)" = false ] || die "the installed host runs the installer"
console_users | grep -q . && die "the installed host logs someone in on a console: $(console_users | tr '\n' ';')" || true
[ "$(vssh cat /proc/sys/kernel/hostname)" = installed ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname), not the installer's"
[ "$(api http://x/api/host-settings | jq -r .host.hostname)" = installed ] || die "the installer's host name is not in the settings"
vssh 'readlink /etc/localtime' | grep -q Europe/Stockholm || die "the installer's time zone is not set"
ssh -q -i "$vm/key2" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -p "$sshport" root@127.0.0.1 true ||
  die "the SSH key given to the installer does not let root in"
vssh 'systemctl --failed --no-legend' | grep . && die "failed units" || true
curl -sf "http://127.0.0.1:$httpport/" | grep -q '<html' || die "the dashboard does not answer over HTTP"
[ "$(api http://x/api/host-settings | jq -r .managed)" = image ] || die "the daemon does not see an image host"
# Wake-on-LAN: the wired card takes the image's link file under the name it
# would have had, and the settings show its MAC address. (A version before
# the link file has neither.)
wol=
if vssh 'test -e /usr/lib/systemd/network/80-wired.link'; then
  nic=$(vssh 'ls /sys/class/net' | grep -E '^(en|eth)' | head -n1)
  link=$(vssh "udevadm info -q property /sys/class/net/$nic")
  grep -qx 'ID_NET_LINK_FILE=/usr/lib/systemd/network/80-wired.link' <<<"$link" || die "$nic does not take 80-wired.link: $(grep LINK_FILE <<<"$link")"
  grep -qx "ID_NET_NAME_PATH=$nic" <<<"$link" || die "the wired card is named $nic, not by its path: $(grep ID_NET_NAME <<<"$link" | tr '\n' ' ')"
  mac=$(vssh "cat /sys/class/net/$nic/address")
  api http://x/api/host-settings | jq -e --arg m "$mac" '.wired[] | select(.mac == $m)' >/dev/null || die "the settings do not show $nic's MAC address $mac"
  wol="; $nic takes 80-wired.link (Wake-on-LAN) under its own name, its MAC address in the settings"
fi
put host-settings/domain "{\"domain\":\"$domain\"}" || die "domain refused"
for _ in $(seq 1 15); do served 2>/dev/null && break; sleep 2; done
served || die "the dashboard is not served at homelab.$domain"
signed=" (no sign-in on $version)"
hassignin && signin && signed=" behind its sign-in"
ok "daemon $(api http://x/api/version | jq -r .version), dashboard over HTTP and at https://homelab.$domain$signed$wol"

fixedids && ok "caddy, homelab-daemon and homelab-nixd have their fixed ids"

step "5. a bcachefs pool through the daemon"
vssh 'bcachefs format -q --replicas=2 /dev/vdb /dev/vdc >/dev/null'
# Pools mount under /var/mnt, by name. A release from before that (0.2.5,
# the first version of a check that updates from it) has /pool in its image
# and takes a path.
pool=/var/mnt/pool guarded=
if vssh 'test -L /mnt'; then
  api -X POST http://x/api/storage/mount -d "'{\"devices\":[\"/dev/vdb\",\"/dev/vdc\"]}'" >/dev/null || die "mount refused"
  vssh "findmnt -no FSTYPE $pool" | grep -q bcachefs || die "$pool is not mounted"
  vssh 'touch /mnt/pool/.check && rm /var/mnt/pool/.check' || die "/mnt/pool is not the pool"
  code=$(vssh "curl -s -o /dev/null -w '%{http_code}' --unix-socket /run/homelab-daemon/daemon.sock -H 'Content-Type: application/json' -X POST http://x/api/storage/mount -d '{\"devices\":[\"/dev/vdb\"],\"name\":\"../x\"}'")
  [ "$code" = 422 ] || [ "$code" = 400 ] || die "a pool name with a path in it answered $code"
  # Unmounted, its directory takes no writes: they would land on /var.
  api -X POST http://x/api/storage/unmount -d "'{\"mountpoint\":\"$pool\"}'" >/dev/null || die "unmount refused"
  vssh "! touch $pool/x 2>/dev/null" || die "the unmounted $pool can be written to"
  api -X POST http://x/api/storage/mount -d "'{\"devices\":[\"/dev/vdb\",\"/dev/vdc\"]}'" >/dev/null || die "the second mount was refused"
  vssh "findmnt -no FSTYPE $pool" | grep -q bcachefs || die "$pool is not mounted again under the name it had"
  guarded="; unmounted, its directory takes no writes"
else
  pool=/pool
  api -X POST http://x/api/storage/mount -d "'{\"devices\":[\"/dev/vdb\",\"/dev/vdc\"],\"mountpoint\":\"/pool\"}'" >/dev/null || die "mount refused"
  vssh 'findmnt -no FSTYPE /pool' | grep -q bcachefs || die "/pool is not mounted"
fi
ok "pool mounted at $pool ($(vssh 'bcachefs version'))$guarded"

# A partition: a file system that is not a pool (vdd, formatted here). Only
# it is offered, not the ESP, the root slots, /var or the pool's disks; it
# mounts under its label's name, and is remembered for the next boot.
part=
if api http://x/api/storage | jq -e 'has("partitions")' >/dev/null; then
  vssh 'mkfs.ext4 -q -L "Check Data" /dev/vdd'
  for _ in $(seq 1 10); do
    st=$(api http://x/api/storage)
    [ "$(jq -r '[.partitions[].path] | join(" ")' <<<"$st")" = /dev/vdd ] && break
    sleep 2
  done
  [ "$(jq -r '[.partitions[] | "\(.path) \(.fstype) \(.state) \(.mount_name)"] | join(", ")' <<<"$st")" = "/dev/vdd ext4 unmounted check-data" ] ||
    die "the partitions offered are not just vdd: $(jq -c .partitions <<<"$st")"
  partuuid=$(jq -r '.partitions[0].uuid' <<<"$st")
  part=/var/mnt/check-data
  api -X POST "http://x/api/storage/partitions/$partuuid/mount" -d "'{}'" >/dev/null || die "the partition's mount was refused"
  vssh "findmnt -no FSTYPE,OPTIONS $part" | grep -q '^ext4 .*nosuid' || die "$part is not mounted: $(vssh "findmnt -no FSTYPE,OPTIONS $part")"
  code=$(vssh "curl -s -o /dev/null -w '%{http_code}' --unix-socket /run/homelab-daemon/daemon.sock -H 'Content-Type: application/json' -X POST http://x/api/storage/partitions/$partuuid/mount -d '{\"name\":\"pool\"}'")
  [ "$code" = 409 ] || die "mounting a mounted partition answered $code"
  api -X POST "http://x/api/storage/partitions/$partuuid/unmount" >/dev/null || die "the partition's unmount was refused"
  vssh "! touch $part/x 2>/dev/null" || die "the unmounted $part can be written to"
  api -X POST "http://x/api/storage/partitions/$partuuid/mount" -d "'{}'" >/dev/null || die "the partition's second mount was refused"
  vssh "echo kept > $part/file"
  ok "partition vdd mounted at $part, the only one offered; unmounted, its directory takes no writes"
fi

step "6. a stack from the drafts template"
vssh "mkdir -p /var/lib/stack-src/site"
for f in homelab-stack.json flake.nix; do
  sed 's/NAME/site/g' "$root/apps/core/internal/drafts/template/$f" | vssh "cat > /var/lib/stack-src/site/$f"
done
vssh 'cd /var/lib/stack-src/site && git init -q && git add -A && git -c user.name=check -c user.email=check@example.com commit -qm site'
rev=$(vssh 'git -C /var/lib/stack-src/site rev-parse HEAD')
api -X POST http://x/api/stacks -d "'{\"name\":\"site\",\"source\":\"git+file:///var/lib/stack-src/site\"}'" >/dev/null || die "install refused"
# The stack's page makes the folder: a subvolume on the pool, owned by the
# host's ids for the user and group the template declares (nginx, the first
# name, so 2000). A version from before that takes a folder made by hand.
madefolder=
if api http://x/api/stacks-ids >/dev/null 2>&1; then
  api -X POST http://x/api/stacks/site/variables/SITE_DIR/folder -d "'{\"place\":\"$pool\",\"folder\":\"site\"}'" >/dev/null || die "the folder was not made"
  [ "$(vssh "stat -c %u:%g $pool/site")" = 2000:2000 ] || die "$pool/site is owned by $(vssh "stat -c %u:%g $pool/site"), not the stack ids 2000:2000"
  vssh "bcachefs subvolume list $pool" | grep -qw site || die "$pool/site is not a subvolume"
  [ "$(api http://x/api/stacks/site | jq -r '.variables[] | select(.name == "SITE_DIR") | "\(.value) \(.owner_state)"')" = "$pool/site ok" ] || die "SITE_DIR is not the made folder on the stack ids"
  madefolder="; its folder a subvolume made from the API, owned by the stack ids"
else
  vssh "mkdir -p $pool/site && chown 2000:2000 $pool/site"
  api -X PUT http://x/api/stacks/site/variables/SITE_DIR -d "'{\"value\":\"$pool/site\"}'" >/dev/null || die "variable refused"
fi
vssh "echo '<p>image-check</p>' > $pool/site/index.html"
api -X POST http://x/api/stacks/site/approve -d "'{\"rev\":\"$rev\"}'" >/dev/null || die "approve refused"
api -X POST http://x/api/stacks/site/start >/dev/null || die "start refused"
for _ in $(seq 1 120); do
  s=$(api http://x/api/stacks/site)
  [ "$(jq -r .running <<<"$s")" = true ] && break
  jq -e '.operation.error // empty' <<<"$s" >/dev/null && die "stack failed: $(jq -r .operation.error <<<"$s")"
  sleep 5
done
[ "$(jq -r .running <<<"$s")" = true ] || die "the stack did not start in 10 minutes"
https site / | grep -q image-check || die "the stack's route is not served at site.$domain"
refused=""
hassignin && stackrefused site && refused="; the dashboard refuses it"
if [ -n "$onvar" ]; then
  st=$(api http://x/api/stacks-store)
  jq -e '.total_bytes > 0 and .free_bytes > 0 and (.own_partition | not)' <<<"$st" >/dev/null || die "the Nix store's disk is not reported: $st"
fi
[ -z "$madefolder" ] || grep -q '^nginx:x:2000:' <(vssh 'cat /var/lib/homelab/appdata/stacks/site/root/etc/passwd') || die "nginx in the stack does not have the id 2000"
ok "stack site built, serving the page from the pool at https://site.$domain$refused$madefolder"
pool_lifecycle

step "7. host settings: VPN, tunnel, firewall, name and time zone"
peer=$(vssh 'wg genkey | wg pubkey')
put host-settings/wireguard "$(jq -n --arg k "$peer" '{enabled: true, address: "10.10.0.1/24", endpoint: "vpn.example.net:51820",
  peers: [{name: "laptop", public_key: $k, address: "10.10.0.2"}]}')" || die "VPN refused"
# A device whose keys the host makes: its config holds the private key that
# belongs to the public key the VPN now has.
dev=$(post host-settings/wireguard/peers '{"name":"phone"}') || die "adding a VPN device refused: $dev"
jq -e '.config | test("Address = 10.10.0.3/32") and test("Endpoint = vpn.example.net:51820")' <<<"$dev" >/dev/null || die "the new device's config: $dev"
devpub=$(jq -r '.config | capture("PrivateKey = (?<k>[^\n]+)").k' <<<"$dev" | vssh wg pubkey)
jq -e --arg k "$devpub" '.settings.wireguard.peers | any(.public_key == $k)' <<<"$dev" >/dev/null || die "the new device's private key is not its peer's"
creds='{"AccountTag":"acc","TunnelID":"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0","TunnelSecret":"c2VjcmV0"}'
put host-settings/tunnel "$(jq -n --arg c "$creds" '{enabled: true, credentials: $c, routes: [{hostname: "site.example.net", target: "site"}]}')" || die "tunnel refused"
put host-settings/firewall '{"openings":[{"port":8443,"proto":"tcp","from":["10.0.2.0/24"]}]}' || die "firewall refused"
put host-settings/host '{"hostname":"checkhost","timezone":"Europe/Stockholm"}' || die "host refused"
errs=$(api http://x/api/host-settings | jq -r '.applied.errors | join("; ")')
[ -z "$errs" ] || die "not applied: $errs"
sleep 2
hostcheck
ok "VPN devices (one with keys the host made), tunnel route, firewall, host name and time zone in place"

step "8. after a reboot"
reboot_vm
booted "$version"
hostcheck
[ -z "$part" ] || [ "$(vssh "cat $part/file")" = kept ] || die "the partition is not mounted at $part after a reboot"
[ "$(vssh 'cat /etc/machine-id')" = "$installed" ] || die "the machine ID changed over a reboot"
ok "everything still in place, the machine ID too (the stack's route ${sitewait}s after the daemon)"

if [ -n "$next" ]; then
  step "9. $next downloads from a release server, then a core update to it through the API"
  rm -rf "$vm/gnupg" && mkdir -m 700 "$vm/gnupg"
  gpg --homedir "$vm/gnupg" --batch --quiet --passphrase '' --quick-gen-key 'image check <check@example.com>' ed25519 sign never
  gpg --homedir "$vm/gnupg" --armor --export > "$vm/release.asc"
  mkdir -p "$vm/release"
  python3 -m http.server --bind 127.0.0.1 --directory "$vm/release" "$relport" >"$vm/release.log" 2>&1 &
  relpid=$!
  vscp "$vm/release.asc" root@127.0.0.1:/var/lib/homelab/release.asc
  vssh "printf 'HOMELAB_RELEASE_URL=http://10.0.2.2:$relport\nHOMELAB_RELEASE_KEYRING=/var/lib/homelab/release.asc\n' > /var/lib/homelab/daemon.env && systemctl restart homelab-daemon"
  for _ in $(seq 1 30); do api http://x/api/version >/dev/null 2>&1 && break; sleep 2; done
  # Sums changed after they were signed: refused, nothing staged.
  release "$next"
  echo "0000000000000000000000000000000000000000000000000000000000000000  homelab_$next.efi" >> "$vm/release/$next/SHA256SUMS"
  d=$(op system-updates/image/download)
  [ "$(jq -r .result <<<"$d")" = failed ] && jq -e '.reason | contains("not signed")' <<<"$d" >/dev/null || die "a tampered release was not refused: $d"
  [ "$(api http://x/api/system-updates/image | jq '.available + .core.available | length')" = 0 ] || die "a tampered release was staged"
  release "$next"
  d=$(op system-updates/image/download)
  [ "$(jq -r .result <<<"$d")" = healthy ] && [ "$(jq -r .version <<<"$d")" = "$next" ] || die "download: $(jq -r '.result + ": " + .reason' <<<"$d")"
  api http://x/api/system-updates/image | jq -e --arg v "$next" '(.available | index($v)) and (.core.available | index($v)) and .release.latest == $v' >/dev/null ||
    die "$next is not staged after its download"
  ok "a tampered release refused; $next downloaded, verified and staged"
  since=$(vssh 'machinectl show site -p Timestamp --value')
  d=$(op system-updates/image/core)
  [ "$(jq -r .result <<<"$d")" = healthy ] || die "core update: $(jq -r '.result + ": " + .reason' <<<"$d")"
  [ "$(api http://x/api/system-updates/image | jq -r .core.active)" = "$next" ] || die "core $next is not active"
  [ "$(vssh 'machinectl show site -p Timestamp --value')" = "$since" ] || die "the stack's container was restarted"
  hostcheck
  # A core update restarts Caddy too: an older version's open API is gated now.
  signed=""
  hassignin && signin && stackrefused site && signed="; the dashboard behind its sign-in, refused to the stack"
  ok "core $next active, no reboot, the stack's container untouched$signed"

  step "10. an image update to $next through the API"
  op system-updates/image/install '{"reboot":"now"}' >/dev/null || true
  # The install reboots the host: wait for it to go, then come back.
  for _ in $(seq 1 60); do vssh true 2>/dev/null || break; sleep 2; done
  up
  for _ in $(seq 1 30); do api http://x/api/version >/dev/null 2>&1 && break; sleep 2; done
  booted "$next"
  vssh 'systemd-sysext status --no-legend' | grep -q homelab-core || die "the core extension is not merged on $next"
  fixedids || true
  # $next has no /pool: a pool the first version mounted there is mounted
  # again by hand, under its name, and the stack is given its new path.
  if [ "$pool" = /pool ] && vssh 'test -L /mnt'; then
    pool=/var/mnt/pool
    api -X POST http://x/api/storage/mount -d "'{\"devices\":[\"/dev/vdb\",\"/dev/vdc\"]}'" >/dev/null || die "mount refused on $next"
    api -X PUT http://x/api/stacks/site/variables/SITE_DIR -d "'{\"value\":\"$pool/site\"}'" >/dev/null || die "variable refused on $next"
    api -X POST http://x/api/stacks/site/stop >/dev/null 2>&1 || true
    for _ in $(seq 1 30); do [ "$(api http://x/api/stacks/site | jq -r '.operation.running // false')" = false ] && break; sleep 2; done
    api -X POST http://x/api/stacks/site/start >/dev/null || die "start refused on $next"
  fi
  hostcheck
  ok "booted $next with everything in place"
  # The way back by hand: the version before is still in its slot.
  st=$(post system-updates/image/boot "{\"version\":\"$version\",\"mode\":\"default\"}") || die "making $version the default was refused: $st"
  [ "$(jq -r .default <<<"$st")" = "$version" ] || die "$version is not the default: $(jq -c '{default, oneshot}' <<<"$st")"
  st=$(post system-updates/image/boot '{"version":"","mode":"default"}') || die "clearing the default was refused: $st"
  [ "$(jq -r '.default // ""' <<<"$st")" = "" ] || die "the default is still set: $(jq -c '{default, oneshot}' <<<"$st")"
  [ "$(post system-updates/image/boot '{"version":"0.0.0.1","mode":"once"}' | jq -r .code)" = no_such_version ] || die "a version that is not installed was accepted"
  st=$(post system-updates/image/boot "{\"version\":\"$version\",\"mode\":\"once\",\"reboot\":true}") || die "booting $version once was refused: $st"
  [ "$(jq -r .oneshot <<<"$st")" = "$version" ] || die "$version is not set to boot once: $(jq -c '{default, oneshot}' <<<"$st")"
  gone
  up
  daemon || die "the daemon does not answer on $version"
  booted "$version"
  reboot_vm
  booted "$next"
  hostcheck
  ok "$version booted once more from the API, then $next again"
  pool_lifecycle
else
  step "9-10. core and image updates skipped: image/out has one version"
fi

if [ -n "$broken" ]; then
  step "11. $broken installs in one operation; its broken image falls back to $next"
  # A downloaded core is in use after a restart, unless it names a newer
  # image than the host runs. (An image from before that leaves it staged.)
  atboot=
  if vssh 'test -x /usr/lib/homelab/core-at-boot'; then
    atboot=1
    need=$next.1
    rm -rf "$vm/core" && fsck.erofs --extract="$vm/core" "$out/core/homelab-core_$next.raw" >/dev/null
    grep -q '^HOMELAB_MIN_IMAGE=' "$vm/core/usr/lib/extension-release.d/extension-release.homelab-core" || die "core $next names no minimum image"
    sed -i 's/^HOMELAB_MIN_IMAGE=.*/HOMELAB_MIN_IMAGE=9999/' "$vm/core/usr/lib/extension-release.d/extension-release.homelab-core"
    mkfs.erofs -zlz4hc --all-root -T 1 "$vm/homelab-core_$need.raw" "$vm/core" >/dev/null
    vscp "$vm/homelab-core_$need.raw" root@127.0.0.1:/var/lib/homelab/updates/core/
    rm -rf "$vm/core" "$vm/homelab-core_$need.raw"
    d=$(op system-updates/image/core)
    [ "$(jq -r .result <<<"$d")" = failed ] && jq -e '.reason | contains("needs image 9999")' <<<"$d" >/dev/null || die "a core for a newer image was not refused: $d"
    reboot_vm
    booted "$next"
    daemon || die "the daemon does not answer"
    [ "$(api http://x/api/system-updates/image | jq -r .core.active)" = "$next" ] || die "a core for a newer image was applied at boot"
    vssh "rm /var/lib/homelab/updates/core/homelab-core_$need.raw"
    vscp "$out/core/homelab-core_$broken.raw" root@127.0.0.1:/var/lib/homelab/updates/core/
    reboot_vm
    booted "$next"
    daemon || die "the daemon does not answer on the core applied at boot"
    [ "$(api http://x/api/system-updates/image | jq -r .core.active)" = "$broken" ] || die "the downloaded core $broken is not in use after a restart: $(vssh 'journalctl -b -u homelab-core-update --no-pager | tail -5')"
    vssh 'systemctl --failed --no-legend' | grep . && die "failed units" || true
    ok "a core for a newer image refused and left staged; the downloaded core $broken in use after a restart"
  fi
  stage_image "$broken"
  vscp "$out/core/homelab-core_$broken.raw" root@127.0.0.1:/var/lib/homelab/updates/core/
  d=$(op system-updates/image/release)
  [ "$(jq -r .result <<<"$d")" = healthy ] && [ "$(jq -r .version <<<"$d")" = "$broken" ] || die "$broken was not installed: $d"
  api http://x/api/system-updates/image | jq -e --arg v "$broken" '.core.active == $v and .pending_reboot' >/dev/null ||
    die "after the install: $(api http://x/api/system-updates/image | jq -c '{core: .core.active, pending_reboot, installed}')"
  ok "image $broken in the free slot and core $broken running, from one call"
  # Corrupt the new slot's root: its verity check fails, so does the boot.
  vssh bash -s "$broken" <<'EOS' || die "could not corrupt the new slot"
set -e
# sysupdate has just labelled the slot: let udev catch up.
for _ in $(seq 1 15); do
  udevadm settle
  root=$(lsblk -lnpo PATH,PARTLABEL,PARTTYPE | awk -v l="homelab_$1" '$2 == l && $3 == "4f68bce3-e8cd-4db1-96e7-fbcaf984b709" {print $1}')
  [ -n "$root" ] && break
  sleep 2
done
test -n "$root"
dd if=/dev/urandom of="$root" bs=1M count=8 conv=notrunc,fsync status=none
EOS
  vssh 'systemctl reboot' || true
  for _ in $(seq 1 30); do vssh true 2>/dev/null || break; sleep 1; done
  # Three failed tries of the broken version, then the one before.
  for _ in $(seq 1 120); do vssh true 2>/dev/null && break; sleep 3; done
  up
  for _ in $(seq 1 30); do api http://x/api/version >/dev/null 2>&1 && break; sleep 2; done
  booted "$next"
  [ "$(api http://x/api/system-updates/image | jq -r .fell_back_from)" = "$broken" ] || die "the daemon does not say it fell back from $broken"
  hostcheck
  ok "back on $next by itself; the daemon reports the fall back from $broken"

  # A daemon that cannot start (a file where its state folder was), on a
  # version that was good before: the boot check tries the core before,
  # fails, and leaves the host up.
  vssh 'systemctl stop homelab-daemon && mv /var/lib/homelab-daemon /var/lib/homelab-daemon.keep && touch /var/lib/homelab-daemon'
  vssh 'systemctl reboot' || true
  for _ in $(seq 1 30); do vssh true 2>/dev/null || break; sleep 1; done
  up
  bootid=$(vssh 'cat /proc/sys/kernel/random/boot_id')
  for _ in $(seq 1 120); do [ "$(vssh 'systemctl is-failed homelab-boot-check.service' 2>/dev/null)" = failed ] && break; sleep 5; done
  [ "$(vssh 'systemctl is-failed homelab-boot-check.service')" = failed ] || die "the boot check did not fail with the daemon broken: $(vssh 'systemctl is-active homelab-boot-check.service')"
  sleep 30
  [ "$(vssh 'cat /proc/sys/kernel/random/boot_id')" = "$bootid" ] || die "the host restarted after the boot check failed on a good version"
  vssh 'grep -q "daemon does not answer" /run/homelab/boot-check-failed' || die "no reason left: $(vssh 'cat /run/homelab/boot-check-failed')"
  [ "$(vssh 'readlink /var/lib/extensions/homelab-core.raw')" = "../extensions.d/homelab-core_$broken.raw" ] ||
    die "the core extension was not put back: $(vssh 'readlink /var/lib/extensions/homelab-core.raw')"
  vssh 'rm /var/lib/homelab-daemon && mv /var/lib/homelab-daemon.keep /var/lib/homelab-daemon && systemctl restart homelab-daemon'
  daemon || die "the daemon does not answer after its repair"
  api http://x/api/system-updates/image | jq -e '.boot_check | contains("daemon does not answer")' >/dev/null || die "the daemon does not report the failed boot check"
  ok "a daemon that cannot start: the host stayed up with the boot check failed, and says why once repaired"

  # A core extension that does not start (its file overwritten): the boot
  # check merges the one before.
  vssh "dd if=/dev/urandom of=/var/lib/extensions.d/homelab-core_$broken.raw bs=1M count=4 conv=notrunc,fsync status=none"
  vssh 'systemctl reboot' || true
  for _ in $(seq 1 30); do vssh true 2>/dev/null || break; sleep 1; done
  up
  daemon 150 || die "the daemon does not answer on the core before"
  booted "$next"
  api http://x/api/system-updates/image | jq -e --arg b "$broken" --arg n "$next" '.core.fell_back_from == $b and .core.active == $n and (.boot_check // "") == ""' >/dev/null ||
    die "after a broken core: $(api http://x/api/system-updates/image | jq -c '{core, boot_check}')"
  hostcheck
  again=
  if [ -n "$atboot" ]; then
    # No boot tries that core again by itself.
    vssh "grep -qFx '$broken' /var/lib/homelab/core-failed" || die "the core that did not start is not written down"
    reboot_vm
    booted "$next"
    daemon || die "the daemon does not answer"
    [ "$(api http://x/api/system-updates/image | jq -r .core.active)" = "$next" ] || die "the core that did not start was used again: $(vssh 'journalctl -b -u homelab-core-update --no-pager | tail -5')"
    again=", and not tried again at the next boot"
  fi
  ok "a core that does not start: core $next back at boot, and the daemon reports it$again"
else
  step "11. fall back skipped: image/out has fewer than three versions"
fi

if [ "${BUILD:-}" = 1 ]; then
  step "12. the host builds an update from main"
  git -C "$root" bundle create "$vm/src.bundle" HEAD 2>/dev/null
  vscp "$vm/src.bundle" root@127.0.0.1:/var/lib/homelab/src.bundle
  vssh 'echo HOMELAB_CORE_SOURCE=git+file:///var/lib/homelab/src.bundle > /var/lib/homelab/daemon.env && systemctl restart homelab-daemon'
  for _ in $(seq 1 30); do api http://x/api/version >/dev/null 2>&1 && break; sleep 2; done
  # The memory the whole host uses while it builds: what a host that builds
  # its own updates needs.
  vssh 'cat > /var/tmp/memwatch.sh' <<'EOS'
while :; do free -m | awk '/^Mem/ {print $3}' >> /var/tmp/mem.log; sleep 2; done
EOS
  vssh 'systemd-run -q --unit=memwatch sh /var/tmp/memwatch.sh'
  start=$SECONDS
  d=$(OPWAIT=720 op system-updates/image/build)
  [ "$(jq -r .result <<<"$d")" = healthy ] || die "build: $(jq -r '.result + ": " + .reason' <<<"$d")"
  built=$(jq -r .version <<<"$d")
  api http://x/api/system-updates/image | jq -e --arg v "$built" '.core.available + .available | index($v)' >/dev/null || die "$built is not staged"
  vssh 'systemctl stop memwatch'
  ok "built and staged $built in $(((SECONDS - start) / 60)) min; memory in use peaked at $(vssh 'sort -n /var/tmp/mem.log | tail -1') of $mem MiB"
fi

step "13. the free-space install boots alone, beside the other system"
stop
drop disk pool1 pool2 part pool3 pool4 pool5
boot "$vm/beside.raw"
booted "$version"
runs_from vda
ownid
[ "$(vssh cat /proc/sys/kernel/hostname)" = beside ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname)"
[ "$(espfiles /dev/vda1)" = "WINDOWS-BOOTMGR WINDOWS-FALLBACK " ] || die "the other system's loaders changed: $(espfiles /dev/vda1)"
vssh 'lsblk -no PARTTYPENAME /dev/vda' | grep -q "Microsoft basic data" || die "the other system's partition is gone"
vssh 'bootctl list' | grep -q 'id: auto-windows' || die "the boot menu has no Windows entry"
vssh 'grep -q "^Address=10.0.2.15/24" /etc/systemd/network/10-homelab-wired.network' || die "the installer's static address is not set"
vssh 'ip -4 -o addr show' | grep -q 'inet 10.0.2.15/24 .* global' || die "the static address is not on the interface"
vssh 'bootctl list' | grep -B1 'id: homelab_' | grep -q '(default)' || die "the system is not the boot menu's default"
ok "boots alone with its static address, as the menu's default next to Windows Boot Manager; its loaders and partition untouched"

step "14. $under: a blank disk whose boot loader goes on another disk's ESP"
stop
drop beside
# From here on the stick is the version under test.
if [ "$under" != "$version" ]; then
  cp --sparse=always "$out/homelab_$under.raw" "$vm/stick.raw"
  medium=$(sfdisk --disk-id "$vm/stick.raw" | tr A-F a-f)
fi
boot "$vm/stick.raw" "$vm/sys.raw" "$vm/win2.raw"
booted "$under"
install vdb true crossdisk "" vdc1
# The stick stays in, now as a USB stick. The firmware's boot order has the
# install first, as the installer left it.
boot "$vm/sys.raw" "$vm/win2.raw" "usb:$vm/stick.raw"
booted "$under"
runs_from vda vdb
fixedids || true
ownid
crossdisk=$id
# Where this version keeps the store, for step 19.
onvar=
vssh 'test -e /usr/lib/systemd/system-generators/homelab-nix' && onvar=1
[ "$(vssh cat /proc/sys/kernel/hostname)" = crossdisk ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname)"
vssh 'ls /boot/EFI/Linux/ >/dev/null; findmnt -no SOURCE /boot' | grep -q '^/dev/vdb1$' || die "/boot is not the other disk's ESP: $(vssh 'findmnt -no SOURCE /boot')"
vssh 'ls /boot/EFI/Linux/' | grep -q "^homelab_$under.efi$" || die "the kernel is not on the other disk's ESP"
vssh 'lsblk -no PARTTYPENAME /dev/vda' | grep -q "EFI System" && die "the system disk got an ESP of its own"
vssh 'bootctl list' | grep -q 'id: auto-windows' || die "the boot menu has no Windows entry"
vssh 'bootctl list' | grep -B1 'id: homelab_' | grep -q '(default)' || die "the system is not the boot menu's default"
[ "$(espfiles /dev/vdb1)" = "WINDOWS-BOOTMGR WINDOWS-FALLBACK " ] || die "the other system's loaders changed: $(espfiles /dev/vdb1)"
ok "boots from the other disk's ESP, mounted at /boot; Windows in its menu, its loaders untouched"

step "15. that boot had the stick left in"
[ -n "$(stickname)" ] || die "the stick is not among the VM's disks"
for _ in $(seq 1 30); do api http://x/api/version >/dev/null 2>&1 && break; sleep 2; done
[ "$(api http://x/api/installer | jq -r .active)" = false ] || die "the installed host runs the installer with the stick in"
[ "$(vssh cat /proc/sys/kernel/hostname)" = crossdisk ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname)"
[ "$(vssh 'cat /etc/machine-id')" = "$crossdisk" ] || die "the machine ID changed with the stick in"
vssh 'ls /boot/EFI/Linux/ >/dev/null; findmnt -no SOURCE /boot' | grep -q '^/dev/vdb1$' || die "/boot is not the other disk's ESP: $(vssh 'findmnt -no SOURCE /boot')"
console_users | grep -q . && die "the installed host logs someone in on a console with the stick in: $(console_users | tr '\n' ';')" || true
vssh 'systemctl --failed --no-legend' | grep . && die "failed units" || true
ok "runs from its own disk and the other disk's ESP, the stick ($(stickname)) unused; nobody logged in on a console"

step "16. the stick boots next to that install, installs over it and restarts into the new system"
# A machine set to start from USB first, and without a firmware UUID. The
# other disk and its ESP are away: the new install brings its own.
stop
drop win2
boot --order --no-uuid "usb:$vm/stick.raw" "$vm/sys.raw"
booted "$under"
stick=$(stickname)
[ -n "$stick" ] || die "the stick is not among the VM's disks"
runs_from "$stick"
st=$(installer)
jq -e --arg s "$stick" '.active and (.disks[] | select(.name == $s) | .in_use)
  and (.disks[] | select(.name == "vda") | (.in_use | not) and .erase.ok)' <<<"$st" >/dev/null ||
  die "not the installer, or unexpected disks: $(jq -c '{active, disks: [.disks[] | {name, in_use, erase}]}' <<<"$st")"
autologin
installto vda true again
[ "$(api http://x/api/installer | jq -r .can_restart)" = true ] || die "the installer cannot restart into the new system: $(vssh efibootmgr | tr '\n' ';')"
qemu=$(cat "$vm/qemu.pid")
was=$(vssh 'cat /proc/sys/kernel/random/boot_id')
api -X POST -H "'X-Installer-Token: $token'" http://x/api/installer/restart >/dev/null || true
# The same VM comes back by itself, the stick still first in the order the
# firmware was told.
now=''
for _ in $(seq 1 60); do
  now=$(vssh 'cat /proc/sys/kernel/random/boot_id' 2>/dev/null) && [ "$now" != "$was" ] && break
  sleep 3
done
[ -n "$now" ] && [ "$now" != "$was" ] || die "the machine did not restart (serial log in $vm)"
kill -0 "$qemu" 2>/dev/null || die "the VM turned off instead of restarting"
booted "$under"
for _ in $(seq 1 30); do api http://x/api/version >/dev/null 2>&1 && break; sleep 2; done
[ "$(api http://x/api/installer | jq -r .active)" = false ] || die "the restart started the installer again, not the new system"
[ -n "$(stickname)" ] || die "the stick is not among the VM's disks"
runs_from vda
[ "$(vssh cat /proc/sys/kernel/hostname)" = again ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname)"
console_users | grep -q . && die "the installed host logs someone in on a console with the stick in: $(console_users | tr '\n' ';')" || true
vssh 'systemctl --failed --no-legend' | grep . && die "failed units" || true
ok "the stick ($stick) ran from itself and offered the installed disk; after the install the page's restart brought up the new system from vda, the stick still in"

step "17. the machine ID without a firmware UUID, and one for every install"
ownid
stop
drop stick
# With the storage drivers in its initrd (a version before the image named
# them has not), the system also has to start with its disk behind other
# controllers. The VM's firmware reads none of them, so the boot loader and
# kernel start from a copy of the system's ESP on a virtio disk; the initrd
# then has to find the root and /var behind the controller. This boot
# is the first of them.
ctl() {
  boot --order --no-uuid "$vm/esp.raw" "$1:$vm/sys.raw"
  booted "$under"
  runs_from sda vda
}
drivers=
if inuki "$under" scsi/vmw_pvscsi && inuki "$under" scsi/megaraid/megaraid_sas && inuki "$under" usb/storage/uas; then
  drivers=", its disk behind VMware's paravirtual SCSI"
  read -r espstart espsize < <(sfdisk -J "$vm/sys.raw" |
    jq -r '.partitiontable.partitions[] | select(.type == "C12A7328-F81F-11D2-BA4B-00A0C93EC93B") | "\(.start) \(.size)"')
  qemu-img create -q -f raw "$vm/esp.raw" $(((espsize + 4096) * 512))
  printf 'label: gpt\n2048,%s,U\n' "$espsize" | sfdisk -q "$vm/esp.raw"
  dd if="$vm/sys.raw" of="$vm/esp.raw" bs=512 skip="$espstart" seek=2048 count="$espsize" conv=notrunc,sparse status=none
  ctl pvscsi
else
  boot --no-uuid "$vm/sys.raw"
  booted "$under"
fi
[ "$(vssh 'cat /etc/machine-id')" = "$id" ] || die "the machine ID changed over a boot: $(vssh 'cat /etc/machine-id'), was $id"
[ "$(vssh 'cat /var/lib/homelab/machine-id')" = "$id" ] || die "the machine ID on /var changed over a boot"
vssh 'systemctl --failed --no-legend' | grep . && die "failed units" || true
ok "the same on the next boot$drivers; $(wc -w <<<"$ids") installs, each with its own"

step "18. the system boots with its disk behind other controllers"
if [ -n "$drivers" ]; then
  for c in megasas uas; do
    stop
    ctl "$c"
    [ "$(vssh 'cat /etc/machine-id')" = "$id" ] || die "behind $c another system booted"
    vssh 'systemctl --failed --no-legend' | grep . && die "failed units behind $c" || true
  done
  ok "the same system with its disk behind a MegaRAID and USB UAS"
else
  ok "skipped: $under is from before the image named its storage drivers"
fi

step "19. a disk with a nix partition keeps its store there"
# A host installed before the store moved to /var has a partition named nix
# and its store on it. The disk gets one at its end, formatted from the
# system itself (that boot has no /nix, and says so without restarting).
stop
if [ -n "$onvar" ]; then
  qemu-img resize -q -f raw "$vm/sys.raw" +1G
  sfdisk -q --relocate gpt-bak-std "$vm/sys.raw"
  echo 'type=073D2B22-9F7E-4817-8D45-890277F7DD55, name=nix' | sfdisk -q --append "$vm/sys.raw"
  boot --no-uuid "$vm/sys.raw"
  nixpart=$(vssh 'lsblk -lno NAME,PARTLABEL /dev/vda' | awk '$2 == "nix" { print $1 }')
  [ -n "$nixpart" ] || die "the nix partition is not on the disk: $(vssh 'lsblk -lno NAME,PARTLABEL /dev/vda' | tr '\n' ';')"
  vssh "mkfs.ext4 -q /dev/$nixpart" || die "could not format /dev/$nixpart"
  stop
  boot --no-uuid "$vm/sys.raw"
  booted "$under"
  [ "$(vssh 'findmnt -no SOURCE -M /nix')" = "/dev/$nixpart" ] || die "/nix is not the nix partition: $(vssh 'findmnt -no SOURCE -M /nix')"
  [ "$(vssh 'cat /etc/machine-id')" = "$id" ] || die "another system booted"
  daemon || die "the daemon does not answer"
  [ "$(api http://x/api/stacks-store | jq -r .own_partition)" = true ] || die "the daemon does not see the store's own partition: $(api http://x/api/stacks-store)"
  vssh 'systemctl --failed --no-legend' | grep . && die "failed units" || true
  ok "/nix is the disk's nix partition ($nixpart)"
else
  ok "skipped: $under has the store on a partition of its own already"
fi

# A run that passed needs none of its disks.
stop
drop sys esp
printf '\nimage-check passed: %s%s%s (the disks of the VM took at most %d MB)\n' "$version" "${next:+ → $next}" "${broken:+, fell back from $broken}" "$peak"
