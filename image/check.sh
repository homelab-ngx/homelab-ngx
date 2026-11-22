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
#      and a machine ID of its own, asking for a login; the coding agents'
#      guidance is linked into /etc; a domain set through the host settings
#      serves the dashboard over HTTPS
#   5. a bcachefs pool is formatted and mounted through the daemon, at
#      /var/mnt/pool; unmounted, its directory takes no writes. A disk with
#      another file system is offered as a partition and mounted by name
#   6. a stack (the drafts template) is built by Nix and serves a page from
#      the pool at its route under the domain, and reads a host folder added
#      read-only, which it cannot write. It is forked into a draft, and a
#      file saved there is an update it runs. Frozen, its processes stay
#      where they are and its route stops answering; thawed, the same
#      processes serve it again. A second pool is made from
#      three more disks through the daemon, grown with a disk that held
#      ext4, shrunk by one disk with its files kept, and deleted; the pool
#      the stack uses is not
#   7. host settings: a VPN device, a tunnel route, the firewall, host name
#      and time zone take effect without a rebuild
#   8. all of it holds after a reboot. A frozen stack refuses a reboot
#      from the API; the reboot thaws it, and it starts again at boot.
#      The stack is checked layer by
#      layer each time: the daemon has it running, its link has the host's
#      address, it answers on its own address, its route is served
#   9. the next version is published on a release server (signed with a key
#      of the check's own): a copy whose SHA256SUMS changed after signing is
#      refused, the real one downloads and is staged; then a core update to
#      it through the API, without a reboot, the stack untouched
#  10. an image update to the downloaded version through the API, with a
#      reboot; everything holds, and the stack's link keeps its address
#      when systemd-networkd reloads, also while the stack starts. The
#      version before boots once more when the
#      API is asked, and the newest again after it
#  11. a third version installs in one operation, image and core; its root
#      is corrupted on purpose, so it falls back to the one before by itself,
#      and the daemon says so. On that version, good before: a daemon that
#      cannot start leaves the host up with the boot check failed, not in a
#      restart loop; and a core extension that does not start is replaced at
#      boot by the one before, once. Before all that: a downloaded core is in
#      use after a restart, and one that names a newer image is refused
#  12. with BUILD=1: the host builds an update from main itself, twice; the
#      second downloads no packages, and neither copies mkosi's outputs
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
# The versions are the newest three in image/out, or the ones VERSIONS names,
# in its order (VERSIONS="0.0.1 0.0.2 0.0.3"); the run starts by saying which.
# A release from before a feature has that part of a step left out, and the
# last line lists what was. The newest version that boots (the second, or the
# only one) is the one under test: it has to have every feature, so a part
# left out there fails the run.
# Run inside the image shell (`make image-check`). Each VM disk is deleted
# after the last step that uses it, and a guest's discards free its file's
# blocks: a hosted runner has little room. A run that passes leaves the logs
# in image/out/vm; one that fails leaves the disks it had too, and the VM's
# journal, failed units and daemon log (failed-*.log). KEEP=1 leaves the VM
# running when a step fails. CPUS and MEM (MiB) size the VM: by default a
# small host's 4 CPUs and 4 GB, 6 GB with BUILD=1.
#
# A command for the VM is in single quotes so that the VM's shell expands it
# (SC2016), and `A && B || die` means what it says: die unless both hold
# (SC2015).
# shellcheck disable=SC2016,SC2015
set -Eeuo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
out=$here/out
vm=$out/vm
# refuse WHY: before the first boot, something the run needs is not there.
refuse() {
  echo "image-check: $*" >&2
  exit 1
}
# keepvm: with KEEP=1 a failed run leaves its VM up. A marker, not a
# variable: a check may fail in a subshell ($(op ...)).
keepvm() { if [ "${KEEP:-}" = 1 ] && [ -d "$vm" ]; then touch "$vm/keep"; fi; }
# A command that fails without a check of its own ends the run (set -e), and
# ssh -q and curl -s say nothing: the trap names the line. In a subshell it
# stays quiet, since the line that reads the subshell's answer fails too.
failed() {
  [ "$BASH_SUBSHELL" -eq 0 ] || return 0
  printf '  ✗ line %s: %s (exit %s)\n' "$2" "$3" "$1" >&2
  keepvm
}
trap 'failed "$?" "$LINENO" "$BASH_COMMAND"' ERR

if [ -n "${VERSIONS:-}" ]; then
  read -ra versions <<<"$VERSIONS"
  [ "${#versions[@]}" -le 3 ] || refuse "VERSIONS names more than three versions"
  for v in "${versions[@]}"; do [ -e "$out/homelab_$v.efi" ] || refuse "no image $v in $out"; done
else
  mapfile -t versions < <(for f in "$out"/homelab_*.efi; do [ ! -e "$f" ] || basename "$f" .efi; done | sed 's/^homelab_//' | sort -V)
fi
[ "${#versions[@]}" -gt 0 ] || refuse "no image in $out: run make image first"
# Boot the oldest of the newest three, update to the next, break the last.
n=${#versions[@]}
version=${versions[$((n > 3 ? n - 3 : 0))]} next='' broken=''
[ "$n" -lt 2 ] || next=${versions[$((n > 3 ? n - 2 : 1))]}
[ "$n" -lt 3 ] || broken=${versions[-1]}
# The version under test: the newest that boots. ($broken is only installed
# and broken.) It has every feature the check knows; see has.
latest=${next:-$version}
# Steps 14 on install from a stick of their own and judge that version: the
# one under test where its installer medium was built, not the release
# before it, which is published and cannot be fixed.
under=$version
if [ -n "$next" ] && [ -e "$out/homelab_$next.raw" ]; then under=$next; fi
domain=check.example.com

# Before the first boot: what the run needs, so that no step stops for it
# half an hour in.
missing=''
for t in qemu-system-x86_64 qemu-img sfdisk jq curl ssh scp ssh-keygen mkfs.vfat mmd mcopy gpg python3 fsck.erofs mkfs.erofs flock; do
  command -v "$t" >/dev/null || missing+=" $t"
done
[ -z "$missing" ] || refuse "not on PATH:$missing (make image-check runs this in the image shell)"
[ -e "${HOMELAB_OVMF:-}/OVMF_CODE.fd" ] && [ -e "${HOMELAB_OVMF:-}/OVMF_VARS.fd" ] ||
  refuse "HOMELAB_OVMF does not name the firmware's folder (the image shell sets it)"
[ -r /dev/kvm ] && [ -w /dev/kvm ] || refuse "no KVM: /dev/kvm cannot be opened"
# needs VERSION FILE...: the files of a version that a step reads.
needs() {
  local v=$1 f
  shift
  for f in "$@"; do
    compgen -G "$out/$f" >/dev/null || refuse "image $v is incomplete in $out: no $f (run make image again)"
  done
}
needs "$version" "homelab_$version.raw" "core/homelab-core_$version.raw"
[ -z "$next" ] || needs "$next" "homelab_$next.efi" "homelab_$next.root*.raw" "core/homelab-core_$next.raw"
[ -z "$broken" ] || needs "$broken" "homelab_$broken.efi" "homelab_$broken.root*.raw" "core/homelab-core_$broken.raw"
# One check at a time in image/out: a second would delete the first one's
# disks. The VM and the release server do not hold the lock (9>&-), so a
# check that was killed leaves it free; the VM it left is stopped here, or it
# would keep its memory and the blocks of its deleted disks.
exec 9>"$out/.check.lock"
flock -n 9 || refuse "another image check is running in $out"
if old=$(cat "$vm/qemu.pid" 2>/dev/null) && [ -n "$old" ] && grep -aqF "$vm/" "/proc/$old/cmdline" 2>/dev/null; then
  echo "image-check: stopping the VM an earlier run left (pid $old)"
  kill "$old" 2>/dev/null || true
  for _ in $(seq 1 20); do
    kill -0 "$old" 2>/dev/null || break
    sleep 0.5
  done
  kill -9 "$old" 2>/dev/null || true
fi
echo "image-check: $version is installed${next:+ and updated to $next}${broken:+; $broken is installed and broken}; steps 14 on install $under"
[ -n "$next" ] || echo "image-check: steps 9 and 10 (the updates) are left out: they need a second version"
[ -n "$broken" ] || echo "image-check: step 11 (the fall back) is left out: it needs a third version"
[ "${BUILD:-}" = 1 ] || echo "image-check: step 12 (a build on the host) is left out: it needs BUILD=1"
if [ -z "${VERSIONS:-}" ] && [ "$n" -gt 3 ]; then echo "image-check: older in $out and not used: ${versions[*]:0:n-3}"; fi

# step "N. what": a step's heading, after how long the step before it took;
# scripts/run reads the progress line. The last step's number is read from
# this file.
steps=$(grep -oE '^ *step "[0-9]+' "${BASH_SOURCE[0]}" | grep -oE '[0-9]+$' | sort -n | tail -n1)
stepname='' stepstart=$SECONDS
took() { printf '%d min %02d s' $(((SECONDS - $1) / 60)) $(((SECONDS - $1) % 60)); }
step() {
  roomused
  [ -z "$stepname" ] || printf '  step %s took %s\n' "$stepname" "$(took "$stepstart")"
  stepname=${1%%.*} stepstart=$SECONDS
  printf '\nprogress: %d%%\n▸ %s\n' "$(((${stepname%%-*} - 1) * 100 / steps))" "$*"
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
  keepvm
  exit 1
}
# seen [GREP OPTION...] PATTERN: the pattern is in the input, read to its
# end. grep -q leaves at the first match; the command before it in the pipe
# may then be writing to a closed pipe, and pipefail takes that for a check
# that failed.
seen() { grep "$@" >/dev/null; }
# is WANT COMMAND...: the command prints WANT.
is() { [ "$("${@:2}")" = "$1" ]; }
# wait_for SECONDS COMMAND...: try the command every 2 s until it holds;
# false when it has not within SECONDS, or when the VM is gone. The command
# may fail in any way while it is waited for.
wait_for() {
  local end=$((SECONDS + $1))
  shift
  until "$@" >/dev/null 2>&1; do
    [ "$SECONDS" -lt "$end" ] || return 1
    [ -z "$qemupid" ] || vmalive || return 1
    sleep 2
  done
}
# has WHAT PROBE...: whether the booted version has a feature. A release
# from before it has not, and the part of the step that checks it is left
# out; that is noted for the last line. The version under test has every
# feature, so there a no fails the run. PROBE answers 0 or 1; anything else
# (ssh could not connect, the daemon did not answer) is not an answer, and
# fails the run too: a part must not be left out because a probe broke.
running='' skipped=''
has() {
  local what=$1 rc=0
  shift
  "$@" >/dev/null 2>&1 || rc=$?
  case $rc in
    0) return 0 ;;
    1) ;;
    *) die "could not tell whether $running has $what: $* (exit $rc)" ;;
  esac
  [ "$running" != "$latest" ] || die "$running has no $what, and it is the version under test"
  case "$skipped" in
    *"$what ($running)"*) ;;
    *)
      skipped+="${skipped:+, }$what ($running)"
      printf '  - not checked on %s, which is from before it: %s\n' "$running" "$what"
      ;;
  esac
  return 1
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
# Three ports next to each other, free now. They start at a multiple of ten,
# so another check's three (on a machine that runs two) do not overlap them.
listening() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }
sshport=''
for _ in $(seq 1 50); do
  p=$((20000 + 10 * (RANDOM % 1000)))
  if ! listening "$p" && ! listening $((p + 1)) && ! listening $((p + 2)); then
    sshport=$p
    break
  fi
done
[ -n "$sshport" ] || refuse "found no three free ports on 127.0.0.1"
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
# Every boot has a serial log of its own, named by the boot's number, the
# step and the first disk ($serial).
qemupid='' boots=0 serial=''
vmalive() { [ -n "$qemupid" ] && kill -0 "$qemupid" 2>/dev/null; }
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
  boots=$((boots + 1))
  serial=$vm/serial-$(printf '%02d' "$boots")-step$stepname-$(basename "$first" .raw).log
  qemu-system-x86_64 -machine q35,accel=kvm -cpu host -smp "$cpus" -m "$mem" "${uuid[@]}" \
    -drive "if=pflash,format=raw,readonly=on,file=$HOMELAB_OVMF/OVMF_CODE.fd" \
    -drive "if=pflash,format=raw,file=$vm/vars.fd" "${disks[@]}" \
    -smbios "type=11,value=io.systemd.credential.binary:ssh.authorized_keys.root=$(base64 -w0 < "$vm/key.pub")" \
    -nic "user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:$sshport-:22,hostfwd=tcp:127.0.0.1:$httpport-:80" \
    -display none -serial "file:$serial" \
    -monitor "unix:$vm/monitor.sock,server,nowait" -daemonize -pidfile "$vm/qemu.pid" 9>&-
  qemupid=$(cat "$vm/qemu.pid")
  up
}
# gone: why a wait for the VM ended, for its message.
gone() { if vmalive; then echo "$1"; else echo "QEMU ended"; fi; }
up() { wait_for 480 vssh true || die "$(gone "no SSH after 8 minutes") (serial log $serial)"; }
# A reboot stops the stacks and unmounts the pools first, and logind lets
# that hold it for up to five minutes. So a host that answers SSH may still
# be the boot that was told to go: the boot id says which one it is. mark
# notes the running boot, and newboot waits for another one to answer
# (SECONDS: 20 minutes unless given). restart is both around a plain reboot.
mark() { lastboot=$(vssh 'cat /proc/sys/kernel/random/boot_id') || die "no boot id before the reboot (serial log $serial)"; }
otherboot() {
  local now
  now=$(vssh 'cat /proc/sys/kernel/random/boot_id') && [ -n "$now" ] && [ "$now" != "$lastboot" ]
}
newboot() { wait_for "${1:-1200}" otherboot || die "$(gone "the VM did not start again") (serial log $serial)"; }
restart() {
  mark
  vssh 'systemctl reboot' || true
  newboot "$@"
}
reboot_vm() {
  restart
  # The daemon applies the host settings and mounts the pool after start.
  daemon 240 || die "the daemon does not answer after the reboot"
}
# The keepalive ends a connection the VM's reboot cut: QEMU's port forward
# keeps it open, and without one it waits forever.
# scp has the same options: a copy cut by a reboot would wait the same way.
sshopts=(-q -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
  -o ConnectTimeout=10 -o ServerAliveInterval=5 -o ServerAliveCountMax=3)
vssh() { ssh "${sshopts[@]}" -i "$vm/key" -p "$sshport" root@127.0.0.1 "$@"; }
vscp() { scp "${sshopts[@]}" -i "$vm/key" -P "$sshport" "$@"; }
relpid=''
stop() {
  if [ -n "$relpid" ]; then
    kill "$relpid" 2>/dev/null || true
    relpid=''
  fi
  [ -n "$qemupid" ] && [ ! -e "$vm/keep" ] || return 0
  vssh poweroff 2>/dev/null || true
  for _ in $(seq 1 30); do
    vmalive || break
    sleep 1
  done
  kill "$qemupid" 2>/dev/null || true
  qemupid=''
  rm -f "$vm/qemu.pid"
}
# evidence: what the VM can say about a run that failed, kept next to the
# serial logs: its journal, the units that failed and the daemon's log.
evidence() {
  vmalive || return 0
  if ! vssh true 2>/dev/null; then
    echo "  the VM does not answer SSH; its serial log is $serial" >&2
    return 0
  fi
  vssh 'systemctl --failed --no-legend --plain; echo; systemctl list-jobs --no-legend' >"$vm/failed-units.log" 2>&1
  vssh 'journalctl -b --no-pager -o short-precise -n 20000' >"$vm/failed-journal.log" 2>&1
  vssh 'journalctl -b -u homelab-daemon --no-pager -o cat -n 5000' >"$vm/failed-daemon.log" 2>&1
  echo "  the VM's journal, failed units and daemon log: $vm/failed-*.log" >&2
}
# finish: however the run ends. A failed one (not one that was interrupted)
# keeps the VM's evidence before the VM is turned off.
finish() {
  local rc=$?
  trap - ERR
  set +e
  if [ "$rc" -ne 0 ] && [ "$rc" -lt 128 ]; then evidence; fi
  if [ -e "$vm/keep" ] && vmalive; then echo "  VM kept: ssh -i $vm/key -p $sshport root@127.0.0.1" >&2; fi
  stop
}
trap finish EXIT
# off: wait for the VM to turn itself off.
off() {
  for _ in $(seq 1 60); do
    vmalive || break
    sleep 1
  done
  ! vmalive || die "the VM did not turn off"
  qemupid=''
  rm -f "$vm/qemu.pid"
}
# The daemon's API on its socket, asked from inside the VM. No request waits
# more than ten minutes: one that hangs fails the check then, not the
# workflow at its limit hours later.
sock="--max-time 600 --unix-socket /run/homelab-daemon/daemon.sock -H 'Content-Type: application/json'"
# api [CURL OPTION...] URL: a request; false on an error status, with the
# answer printed all the same.
api() { vssh "curl -s --fail-with-body $sock $*"; }
# send METHOD PATH [BODY] [CURL OPTION...]: a request to /api/PATH. The body
# goes over stdin, so nothing in it meets a shell's quoting.
send() {
  local method=$1 path=$2 body=${3-}
  shift 2
  [ $# -eq 0 ] || shift
  if [ -n "$body" ]; then
    vssh "curl -s --fail-with-body $sock -X $method -d @- $* 'http://x/api/$path'" <<<"$body"
  else
    vssh "curl -s --fail-with-body $sock -X $method $* 'http://x/api/$path'" </dev/null
  fi
}
# ask METHOD PATH [BODY] [CURL OPTION...]: the same, for a request whose
# answer nothing reads: it is printed only when the request fails, as the
# reason.
ask() {
  local r
  r=$(send "$@") || {
    printf '    %s\n' "$r" >&2
    return 1
  }
}
# answers STATUS METHOD PATH [BODY]: a probe for has: the API answers with
# STATUS. A version without the route (404, 405) has not got the feature.
answers() {
  case "$(code "${@:2}")" in
    "$1") return 0 ;;
    404 | 405) return 1 ;;
    *) return 2 ;;
  esac
}
# apihas PATH FILTER: a probe for has: jq's FILTER holds for what GET PATH
# answers.
apihas() {
  local b
  b=$(api "http://x/api/$1") || return 2
  jq -e "$2" <<<"$b" >/dev/null
}
# nofailed [WHERE]: no unit has failed; else which, and what each last said.
nofailed() {
  local units
  units=$(vssh 'systemctl --failed --no-legend --plain') || die "could not list the failed units${1:+ $1}"
  [ -n "$units" ] || return 0
  printf '%s\n' "$units" >&2
  vssh 'systemctl --failed --no-legend --plain | while read -r u _; do echo "== $u"; journalctl -b -u "$u" --no-pager -n 15 -o cat; done' >&2 || true
  die "failed units${1:+ $1}"
}
# nologin [WHEN]: nobody is logged in on a console, as on an installed host.
nologin() {
  local users
  users=$(console_users) || die "could not list the logins on the consoles"
  [ -z "$users" ] || die "the installed host logs someone in on a console${1:+ $1}: $(tr '\n' ';' <<<"$users")"
}
# overhttp: the dashboard's page answers on the host's port 80.
overhttp() {
  local page
  page=$(curl -sf --max-time 30 "http://127.0.0.1:$httpport/") && seen '<html' <<<"$page" || die "the dashboard does not answer over HTTP"
}
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
bothconsoles() {
  local users
  users=$(console_users) && awk '$1 == "root" && $2 == "ttyS0" { serial = 1 } $1 == "root" && $2 != "ttyS0" { screen = 1 }
    END { exit !(serial && screen) }' <<<"$users"
}
autologin() {
  wait_for 60 bothconsoles ||
    die "root is not logged in on both the screen and the serial port: $(console_users | tr '\n' ';')"
}
# installer: the installer's state, once its API answers.
installer() {
  wait_for 120 api http://x/api/installer || die "the installer does not answer"
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
  vssh 'findmnt -no SOURCE /etc/machine-id' | seen '\[/lib/homelab/machine-id\]$' ||
    die "/etc/machine-id is not /var's file bound over it: $(vssh 'findmnt -no SOURCE /etc/machine-id')"
  [ "$id" != "${fwuuid//-/}" ] || die "the machine ID is the firmware's UUID"
  case " $ids " in *" $id "*) die "another install has the machine ID $id too" ;; esac
  ids+=" $id"
}
# put PATH JSON: PUT a body to the daemon's API; a refusal's reason is printed.
put() { ask PUT "$@"; }
# post PATH JSON: the same with POST; prints the answer.
post() { send POST "$@"; }
# code METHOD PATH [BODY]: the status an API request answers with.
code() {
  vssh "curl -s -o /dev/null -w '%{http_code}' $sock -X $1 ${3:+-d @-} 'http://x/api/$2'" <<<"${3-}"
}
# spare: the disks a pool can be made from, as "path:contents" words.
spare() { api http://x/api/storage | jq -r '[.unassigned[] | "\(.path):\(.contents)"] | join(" ")'; }
# pool_lifecycle: a second pool on vde, vdf and vdg, made, grown, shrunk and
# deleted through the daemon. It runs once, on the first version that can:
# a release from before the daemon made pools lists no contents for a disk.
lifecycle=
pool_lifecycle() {
  [ -z "$lifecycle" ] || return 0
  has "pools made, changed and deleted by the daemon" apihas storage '.unassigned | length > 0 and (.[0] | has("contents"))' || return 0
  local st uuid main mainname sum op
  [ "$(spare)" = "/dev/vde: /dev/vdf: /dev/vdg:" ] || die "the spare disks are not vde, vdf and vdg, blank: $(spare)"
  [ "$(code POST storage/pools '{"name":"scratch","disks":["/dev/vde"],"copies":2}')" = 409 ] || die "two copies on one disk were not refused"
  [ "$(code POST storage/pools '{"name":"scratch","disks":["/dev/vda"],"copies":1}')" = 409 ] || die "a pool on the system disk was not refused"
  st=$(post storage/pools '{"name":"scratch","disks":["/dev/vde","/dev/vdf"],"copies":2}') || die "the pool was not made: $st"
  uuid=$(jq -r .uuid <<<"$st")
  [ "$(jq -r '"\(.state) \(.mountdir) \(.data_replicas) \(.auto_mount) \(.disks | length)"' <<<"$st")" = "mounted /var/mnt/scratch 2 true 2" ] ||
    die "the new pool is not mounted at /var/mnt/scratch with 2 copies on 2 disks: $st"
  vssh 'findmnt -no FSTYPE /var/mnt/scratch' | seen bcachefs || die "/var/mnt/scratch is not mounted"

  # A disk with a file system on it is erased only with the pool's name.
  vssh 'mkfs.ext4 -q -L Old /dev/vdg'
  wait_for 30 is '/dev/vdg:ext4 "Old"' spare || die "vdg is not offered with its contents: $(spare)"
  [ "$(code POST "storage/pools/$uuid/disks" '{"disk":"/dev/vdg"}')" = 422 ] || die "a disk with contents was added without the pool's name"
  vssh 'blkid -o value -s TYPE /dev/vdg' | seen ext4 || die "the refused add touched vdg"
  [ "$(code POST "storage/pools/$uuid/disks" '{"disk":"/dev/vdg","confirm":"scratch"}')" = 204 ] || die "vdg was not added"
  [ "$(api http://x/api/storage | jq -r --arg u "$uuid" '.pools[] | select(.uuid == $u) | .disks | length')" = 3 ] || die "the pool does not have 3 disks"

  # Removing a disk moves its data to the others first.
  vssh 'dd if=/dev/urandom of=/var/mnt/scratch/file bs=1M count=300 status=none && sync'
  sum=$(vssh 'sha256sum < /var/mnt/scratch/file')
  [ "$(code DELETE "storage/pools/$uuid/disks?disk=/dev/vde")" = 202 ] || die "the removal of vde did not start"
  op=''
  removed() {
    op=$(api http://x/api/storage | jq -c --arg u "$uuid" '.pools[] | select(.uuid == $u) | .operation') &&
      jq -e '.finished_at' <<<"$op"
  }
  wait_for 300 removed || die "the removal of vde did not end: $op"
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
  vssh "findmnt -no FSTYPE $pool" | seen bcachefs || die "$pool is not mounted any more"
  lifecycle=1
  ok "a pool made on vde and vdf, grown with vdg (erased by name), shrunk by vde with its file kept, and deleted; the stack's pool refused"
}
# daemon [SECONDS]: wait for the daemon's API (2 minutes unless given).
daemon() { wait_for "${1:-120}" api http://x/api/version; }
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
  started=$(send POST "$1" "${2-}") || die "$1 refused: $started"
  id=$(jq -r '.id // empty' <<<"$started") || id=''
  [ -n "$id" ] || die "$1 started no operation: $started"
  for _ in $(seq 1 "${OPWAIT:-180}"); do
    sleep 5
    d=$(api http://x/api/system-updates/deploys 2>/dev/null | jq -c ".[] | select(.id == $id)") || continue
    [ -n "$d" ] && [ "$(jq -r .result <<<"$d")" != running ] && { echo "$d"; return; }
  done
  die "$1 did not finish"
}
# https NAME PATH: GET https://NAME.$domain PATH through Caddy in the VM.
https() { vssh "curl -skf --max-time 30 --resolve $1.$domain:443:127.0.0.1 https://$1.$domain$2"; }

# booted VERSION: the VM runs VERSION (from here on $running, which has asks
# about), its boot check passed, on its verity root.
booted() {
  local got
  got=$(vssh '. /etc/os-release && echo "$IMAGE_VERSION"') || die "could not read the booted version"
  [ "$got" = "$1" ] || die "booted $got, not $1"
  running=$1
  # The boot check waits for the daemon (up to two minutes) before it passes.
  wait_for 240 vssh 'systemctl is-active -q boot-complete.target' || die "boot-complete.target not reached"
  vssh 'findmnt -no SOURCE / | grep -q /dev/mapper/root' || die "/ is not on the verity device"
}

# served: the dashboard's API answers through Caddy at homelab.$domain: 200,
# or 401 on a version with the sign-in. hassignin: the booted version has
# the sign-in (an older release, booted first in a release's check, has not).
apicode() { vssh "curl -sk --max-time 30 -o /dev/null -w '%{http_code}' --resolve homelab.$domain:443:127.0.0.1 https://homelab.$domain$1"; }
served() { case "$(apicode /api/version)" in 200 | 401) ;; *) return 1 ;; esac; }
# A probe for has: no status at all (000, or nothing from ssh) is no answer.
hassignin() {
  case "$(apicode /api/auth)" in
    200) return 0 ;;
    000 | '') return 2 ;;
    *) return 1 ;;
  esac
}

# signin: through Caddy the dashboard's API wants a session; the first visitor
# sets the password, a second cannot, and the CLI can remove it.
signin() {
  local u="https://homelab.$domain" r="--max-time 30 --resolve homelab.$domain:443:127.0.0.1"
  local post="-H 'Origin: $u' -H 'Content-Type: application/json' -d '{\"password\":\"image-check-pw\"}'"
  [ "$(vssh "curl -sk $r $u/api/stacks" | jq -r .code)" = setup_required ] || die "the API answers before a password is set"
  vssh "curl -sk $r -c /tmp/jar $post $u/api/auth/setup" | jq -e .signed_in >/dev/null || die "the first password was refused"
  vssh "curl -skf $r -b /tmp/jar $u/api/stacks" >/dev/null || die "signed in, the API still refuses"
  [ "$(vssh "curl -sk $r $u/api/stacks" | jq -r .code)" = login_required ] || die "the API answers without a session"
  [ "$(vssh "curl -sk $r $post $u/api/auth/setup" | jq -r .code)" = password_set ] || die "a second visitor could set the password"
  vssh 'homelab auth status' | seen 'has a password' || die "the CLI does not see the password"
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
  has "fixed ids for the image's accounts" vssh 'grep -qs "^u caddy 956 " /usr/lib/sysusers.d/homelab.conf || exit 1' || return 1
  got=$(vssh 'echo "$(id -u caddy):$(id -g caddy) $(getent group homelab-daemon | cut -d: -f3) $(id -u homelab-nixd)"')
  [ "$got" = "956:956 957 955" ] || die "the image's accounts have other ids than the ones it fixes: caddy, homelab-daemon, homelab-nixd are $got"
}

# storeonvar: a probe for has: the booted version keeps the Nix store on
# /var. (Its generator came with the reason a 100 MB ESP is refused, which
# step 1 tells by it.)
storeonvar() { vssh 'test -e /usr/lib/systemd/system-generators/homelab-nix'; }

# hasdevicekeys: the booted version makes a VPN device's keys: it answers an
# add without a name with 422; an older release, booted first in a release's
# check, has no such route (404).
hasdevicekeys() { answers 422 POST host-settings/wireguard/peers '{}'; }
# devicekeys: a VPN device whose keys the host makes. Its config holds the
# next address, the endpoint and the private key of the public key the VPN
# now has. vpndevices is how many devices hostcheck expects.
vpndevices=1 made=
devicekeys() {
  local dev devpub
  dev=$(post host-settings/wireguard/peers '{"name":"phone"}') || die "adding a VPN device refused: $dev"
  jq -e '.config | test("Address = 10.10.0.3/32") and test("Endpoint = vpn.example.net:51820")' <<<"$dev" >/dev/null || die "the new device's config: $dev"
  devpub=$(jq -r '.config | capture("PrivateKey = (?<k>[^\n]+)").k' <<<"$dev" | vssh wg pubkey)
  jq -e --arg k "$devpub" '.settings.wireguard.peers | any(.public_key == $k)' <<<"$dev" >/dev/null || die "the new device's private key is not its peer's"
  vpndevices=2 made=" (one with keys the host made)"
}

# The stack after a boot, an update or a change to the network, checked from
# the inside out so that a failure names the layer that broke:
#   - the daemon has the stack running;
#   - the host end of its link, ve-site, has the gateway address (the
#     stack's own address with .1);
#   - nginx in the stack answers on the stack's address;
#   - the page comes through Caddy at its route.
# stackfault prints the first layer that does not hold, or nothing. A
# version from before the daemon reported a stack's address is checked at
# its route only.
stackfault() {
  local s addr gw
  s=$(api http://x/api/stacks/site 2>/dev/null) || { echo "the daemon does not answer for the stack"; return; }
  [ "$(jq -r .running <<<"$s")" = true ] || { echo "the daemon does not have the stack running"; return; }
  addr=$(jq -r '.address // empty' <<<"$s")
  if [ -n "$addr" ]; then
    gw=${addr%.*}.1
    vssh 'ip -4 -o addr show dev ve-site' 2>/dev/null | seen -F " $gw/" || { echo "ve-site does not have the host's address $gw"; return; }
    vssh "curl -sf -m 5 http://$addr:8080/" 2>/dev/null | seen image-check || { echo "the stack does not answer at $addr:8080"; return; }
  fi
  https site / 2>/dev/null | seen image-check || echo "the stack's route is not served at site.$domain"
}
# stackevidence: what each layer looks like, for the log of a failed run.
stackevidence() {
  echo "  the stack, layer by layer:" >&2
  {
    api http://x/api/stacks/site | jq -c '{running, want_running, generation, address, operation}'
    vssh 'ip -4 -o addr show dev ve-site
      systemctl status homelab-stack-site.service --no-pager -n 0
      journalctl -b -u systemd-networkd -o short-precise --no-pager | grep -iE "ve-site|reconfigur" | tail -15
      journalctl -b -u homelab-daemon -o cat --no-pager | grep -E "\"module\":\"(stacks|hostcfg)\"" | grep -vF "\"kind\":\"cmd\"" | tail -15'
  } 2>&1 | sed 's/^/    /' >&2 || true
}
# stackis FILTER WANT: jq's FILTER of the stack, as the daemon has it, is WANT.
stackis() {
  local s
  s=$(api http://x/api/stacks/site) && [ "$(jq -r "$1" <<<"$s")" = "$2" ]
}
# stackrunning LIMIT: the daemon has the stack running within LIMIT seconds.
# After a boot it answers before it has started the stacks again, so this is
# the wait that may be long.
stackrunning() {
  local s w=0
  until s=$(api http://x/api/stacks/site 2>/dev/null) && [ "$(jq -r .running <<<"$s")" = true ]; do
    if jq -e '.operation.error // empty' <<<"$s" >/dev/null 2>&1; then
      stackevidence
      die "the stack failed to start: $(jq -r .operation.error <<<"$s")"
    fi
    [ "$w" -lt "$1" ] || { stackevidence; die "the daemon did not have the stack running within $1 s"; }
    sleep 2
    w=$((w + 2))
  done
  sitewait=$w
}
# stackup LIMIT [WHEN]: every layer holds within LIMIT seconds. A running
# stack is reachable at once, so the limit is short.
stackup() {
  local fault w=0
  while fault=$(stackfault) && [ -n "$fault" ]; do
    [ "$w" -lt "$1" ] || { stackevidence; die "$fault${2:+ $2}"; }
    sleep 2
    w=$((w + 2))
  done
}
# stackfork: the stack moves onto a draft of its own name (a fork of its
# approved commit), a file saved in the draft arrives as an approved update,
# and the stack runs that commit after the update. A version from before
# forks has no such operation, and nothing is checked.
# stackfreeze: the stack frozen keeps its processes and stops answering; the
# daemon and its service list still answer at once; thawed, the same
# processes serve the page. False on a version without freezing.
stackfreeze() {
  local leader st t0 c
  has "freezing a stack" apihas stacks/site 'has("frozen")' || return 1
  leader=$(vssh "machinectl show site -p Leader --value")
  st=$(post stacks/site/freeze '{}') || die "the freeze was refused: $st"
  jq -e '.frozen and .running' <<<"$st" >/dev/null || die "the stack is not frozen after a freeze: $(jq -c '{frozen, running}' <<<"$st")"
  [ "$(vssh 'systemctl show -p FreezerState --value site.scope')" = frozen ] || die "the stack's scope is not frozen"
  # No answer in 5 s (000), or Caddy's own error for a silent backend.
  c=$(vssh "curl -sk -o /dev/null -w '%{http_code}' --max-time 5 --resolve site.$domain:443:127.0.0.1 https://site.$domain/; true") ||
    die "could not ask for the frozen stack's page"
  case $c in '' | 2?? | 3??) die "the frozen stack still answers (${c:-no status})" ;; esac
  t0=$(date +%s)
  api http://x/api/stacks/site | jq -e .frozen >/dev/null || die "the stack does not show as frozen"
  api http://x/api/services >/dev/null || die "the service list fails while a stack is frozen"
  [ $(($(date +%s) - t0)) -le 5 ] || die "the API took $(($(date +%s) - t0))s with a frozen stack"
  [ "$(code POST system/power '{"action":"reboot"}')" = 409 ] || die "a reboot was not refused with a frozen stack"
  post stacks/site/thaw '{}' | jq -e '.frozen | not' >/dev/null || die "the stack is still frozen after a thaw"
  https site / | seen image-check || die "the thawed stack does not serve its page"
  [ "$(vssh "machinectl show site -p Leader --value")" = "$leader" ] || die "the stack restarted over the freeze"
}

stackfork() {
  local d s='' rev w=0
  # A fork of a stack that is not there: the route answers that it knows none.
  forks() {
    local b
    b=$(vssh "curl -s $sock -X POST http://x/api/stacks/nope/fork") || return 2
    [ "$(jq -r '.code // empty' <<<"$b" 2>/dev/null)" = unknown_stack ]
  }
  has "a stack forked into a draft" forks || return 1
  d=$(api -X POST http://x/api/stacks/site/fork) || die "the fork was refused: $d"
  [ "$(jq -r '"\(.forked_from) \(.forked_rev)"' <<<"$d")" = "git+file:///var/lib/stack-src/site $1" ] ||
    die "the draft does not record its origin: $(jq -c '{forked_from, forked_rev}' <<<"$d")"
  d=$(send PUT drafts/site '{"changes":[{"path":"notes/fork.txt","content":"forked\n"}],"message":"Add a note"}') || die "the save was refused: $d"
  rev=$(jq -r .rev <<<"$d")
  s=$(api http://x/api/stacks/site)
  [ "$(jq -r .source <<<"$s")" = git+file:///var/lib/homelab-daemon/drafts/site ] || die "the stack's source is $(jq -r .source <<<"$s")"
  [ "$(jq -r .approved.rev <<<"$s")" = "$rev" ] || die "the saved commit $rev is not approved: $(jq -r .approved.rev <<<"$s")"
  ask POST stacks/site/update || die "update refused"
  until s=$(api http://x/api/stacks/site 2>/dev/null) &&
    jq -e --arg r "$rev" '.generation as $g | .running and .operation.finished_at != null and ([.generations[] | select(.id == $g and .rev == $r)] | length == 1)' <<<"$s" >/dev/null; do
    jq -e '.operation.error // empty' <<<"$s" >/dev/null 2>&1 && die "the update failed: $(jq -r .operation.error <<<"$s")"
    [ "$w" -lt 600 ] || die "the stack did not run $rev within 10 minutes"
    sleep 5
    w=$((w + 5))
  done
  ok "stack site forked into a draft; a saved file arrived as an update and runs (commit ${rev:0:12})"
}

# stackrace: systemd-networkd reading its configuration again leaves the
# stack's link alone. The daemon gives ve-site its address, and networkd
# also has a file for ve-* links, so a reconfigure takes the address off:
# the first change to the host's settings after a boot does one. Checked
# with the stack running, and with reloads going on while it starts. Only on
# the version under test: a release before cannot be fixed by this run.
stackrace() {
  local racer
  # What a change to the host settings runs. The first one after a boot
  # makes networkd configure every link again; later ones do not.
  vssh 'systemd-confext refresh --always-refresh=yes && networkctl reload' || die "confext refresh and networkctl reload refused"
  sleep 3
  stackup 0 "after the host's settings were applied as a change does"
  vssh 'networkctl reconfigure ve-site' || die "networkctl reconfigure refused"
  sleep 3
  stackup 0 "after systemd-networkd reconfigured the link"
  ask POST stacks/site/stop || die "stop refused"
  wait_for 180 stackis .running false || die "the stack did not stop in 3 minutes"
  vssh 'for _ in $(seq 1 60); do networkctl reload; networkctl reconfigure ve-site 2>/dev/null; sleep 0.5; done' &
  racer=$!
  ask POST stacks/site/start || die "start refused"
  stackrunning 300
  wait "$racer" || true
  sleep 3
  stackup 0 "after systemd-networkd reloads during its start"
}

# hostcheck: everything the host settings asked for is in place.
hostcheck() {
  served || die "the dashboard is not served at homelab.$domain"
  stackrunning 300
  stackup 20
  [ "$(vssh 'wg show wg-homelab peers' | wc -l)" -eq "$vpndevices" ] || die "the VPN does not have its $vpndevices devices: $(vssh 'wg show wg-homelab peers' | wc -l)"
  vssh 'ip -4 -o addr show wg-homelab' | seen -F 10.10.0.1/24 || die "the VPN interface has no address"
  vssh 'grep -q "originServerName: site.'"$domain"'" /var/lib/homelab/cloudflared/config.yml' || die "the tunnel route is not in cloudflared's config"
  vssh 'systemctl is-enabled homelab-cloudflared' | seen runtime || die "the tunnel is not turned on"
  vssh 'nft list table inet homelab_host' | seen 'dport 8443 accept' || die "the firewall table is not loaded"
  [ "$(vssh cat /proc/sys/kernel/hostname)" = checkhost ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname)"
  vssh 'readlink /etc/localtime' | seen Europe/Stockholm || die "the time zone is not set"
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
  local st='' planned
  claim
  local plan="{\"disk\":\"$1\",\"erase\":$2${5:+,\"esp\":\"$5\"}${6:+,\"region\":$6}}"
  planned=$(post installer/plan "$plan") || die "no plan for $1: $planned"
  [ "$(jq -r .ok <<<"$planned")" = true ] || die "no plan for $1: $(jq -r .reason <<<"$planned")"
  ask POST installer/install "{\"disk\":\"$1\",\"erase\":$2,\"hostname\":\"$3\",\"timezone\":\"Europe/Stockholm\",\"ssh_keys\":[\"$(cat "$vm/key2.pub")\"]${4:+,\"ipv4\":$4}${5:+,\"esp\":\"$5\"}${6:+,\"region\":$6}}" \
    -H "'X-Installer-Token: $token'" || die "install refused"
  # One poll that fails is not the install's end: the VM is busy writing.
  for _ in $(seq 1 120); do
    sleep 5
    st=$(api http://x/api/installer | jq -r .step) || continue
    [ "$st" = installing ] || break
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
  send POST installer/poweroff '' -H "'X-Installer-Token: $token'" >/dev/null || true
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
  grep -v '^ *#' "$here/base/$f" | seen -i "$medium" || die "the installer medium's disk UUID is $medium, which image/base/$f does not name"
done
boot "$vm/stick.raw" "$vm/disk.raw" "$vm/beside.raw" "$vm/small.raw" "$vm/win100.raw"
booted "$version"
nofailed
[ "$(stickname)" = vda ] || die "the disk with the medium's UUID is $(stickname), not vda"
runs_from vda
st=$(installer)
jq -e '.active and (.disks[] | select(.name == "vda") | .in_use) and (.disks[] | select(.name == "vdb") | .erase.ok)
  and (.disks[] | select(.name == "vdc") | .beside.ok)' <<<"$st" >/dev/null || die "unexpected disks: $(jq -c '[.disks[] | {name, in_use, erase, beside}]' <<<"$st")"
overhttp
autologin
# A disk sold as 64 GB takes an install, where the image's partitions fit it
# (a version before that says it is too small).
small=
if has "an install onto a 64 GB disk" jq -e '.min_bytes != null and .min_bytes <= 64000000000' <<<"$st"; then
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
if has "the reason a 100 MB ESP is refused" storeonvar; then
  jq -r .beside.reason <<<"$tiny" | seen 'Windows makes one of 100 MB' || die "the refusal does not say why: $(jq -r .beside.reason <<<"$tiny")"
  plan=$(post installer/plan '{"disk":"vde","erase":false}')
  [ "$(jq -r .ok <<<"$plan")" = false ] && jq -r .reason <<<"$plan" | seen '400 MB' || die "a plan next to a 100 MB ESP: $plan"
  small="$small; free space next to a 100 MB ESP refused with the reason"
fi
# The installer is one browser's, and not for ever: a second claim is
# refused, the code on the console takes it over, and the token from before
# holds nothing after that. A failed boot check never restarts the stick,
# which has no tries to count. (A version before the take-over has none of
# this.)
held=
if has "the installer's take-over" vssh 'test -e /run/homelab-installer/takeover-code'; then
  code=$(vssh 'cat /run/homelab-installer/takeover-code')
  [[ $code =~ ^[0-9]{6}$ ]] || die "the take-over code is not six digits: $code"
  first=$(api -X POST http://x/api/installer/claim | jq -r .token) || die "the first claim was refused"
  [ "$(api -X POST http://x/api/installer/claim | jq -r .code || true)" = installer_claimed ] || die "a second claim was not refused"
  [ "$(post installer/takeover '{"code":"x"}' | jq -r .code || true)" = invalid_install ] || die "a wrong take-over code was not refused"
  second=$(post installer/takeover "{\"code\":\"$code\"}" | jq -r .token) || die "the console's code did not take the installer over"
  [ "$(api -H "'X-Installer-Token: $second'" http://x/api/installer | jq -r .mine)" = true ] || die "the installer is not the taker's"
  [ "$(api -H "'X-Installer-Token: $first'" http://x/api/installer | jq -r .mine)" = false ] || die "the token from before the take-over still holds the installer"
  # Read whole, then searched (see seen).
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
! sfdisk -d "$vm/beside.raw" | seen homelab-reserved || die "a temporary partition was left on the disk"
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
vssh efibootmgr | seen -i "GPT,$esp," || die "the firmware dropped the first install's boot entry: $(vssh efibootmgr | tr '\n' ';')"
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
if has "the Nix store on /var" storeonvar; then
  onvar=1
  vssh 'findmnt -no SOURCE -M /nix' | seen '\[/nix\]$' || die "/nix is not /var's folder: $(vssh 'findmnt -no SOURCE -M /nix')"
  ! sfdisk -d "$vm/disk.raw" | seen 'name="nix"' || die "the install made a nix partition"
else
  vssh 'mountpoint -q /nix' || die "/nix is not mounted"
fi
vssh 'systemd-sysext status --no-legend' | seen homelab-core || die "the core extension is not merged"
daemon || die "the daemon does not answer"
[ "$(api http://x/api/installer | jq -r .active)" = false ] || die "the installed host runs the installer"
nologin
[ "$(vssh cat /proc/sys/kernel/hostname)" = installed ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname), not the installer's"
[ "$(api http://x/api/host-settings | jq -r .host.hostname)" = installed ] || die "the installer's host name is not in the settings"
vssh 'readlink /etc/localtime' | seen Europe/Stockholm || die "the installer's time zone is not set"
vssh 'grep -q "^# This machine runs homelab-ngx" /etc/claude-code/CLAUDE.md && grep -q "^name: homelab" /etc/claude-code/.claude/skills/homelab/SKILL.md /etc/codex/skills/homelab/SKILL.md && grep -q /usr/share/homelab/agent/brief.md /etc/codex/config.toml /etc/opencode/opencode.json && test -s "$(jq -r ".instructions[0]" /etc/opencode/opencode.json)"' ||
  die "the coding agents' guidance is not reachable from /etc"
ssh "${sshopts[@]}" -i "$vm/key2" -p "$sshport" root@127.0.0.1 true ||
  die "the SSH key given to the installer does not let root in"
nofailed
overhttp
[ "$(api http://x/api/host-settings | jq -r .managed)" = image ] || die "the daemon does not see an image host"
# Wake-on-LAN: the wired card takes the image's link file under the name it
# would have had, and the settings show its MAC address. (A version before
# the link file has neither.)
wol=
if has "Wake-on-LAN for the wired card" vssh 'test -e /usr/lib/systemd/network/80-wired.link'; then
  nic=$(vssh 'ls /sys/class/net' | grep -E '^(en|eth)' | head -n1)
  link=$(vssh "udevadm info -q property /sys/class/net/$nic")
  grep -qx 'ID_NET_LINK_FILE=/usr/lib/systemd/network/80-wired.link' <<<"$link" || die "$nic does not take 80-wired.link: $(grep LINK_FILE <<<"$link")"
  grep -qx "ID_NET_NAME_PATH=$nic" <<<"$link" || die "the wired card is named $nic, not by its path: $(grep ID_NET_NAME <<<"$link" | tr '\n' ' ')"
  mac=$(vssh "cat /sys/class/net/$nic/address")
  api http://x/api/host-settings | jq -e --arg m "$mac" '.wired[] | select(.mac == $m)' >/dev/null || die "the settings do not show $nic's MAC address $mac"
  wol="; $nic takes 80-wired.link (Wake-on-LAN) under its own name, its MAC address in the settings"
fi
put host-settings/domain "{\"domain\":\"$domain\"}" || die "domain refused"
wait_for 60 served || die "the dashboard is not served at homelab.$domain"
signed=" (no sign-in on $version)"
if has "the dashboard's sign-in" hassignin; then
  signin
  signed=" behind its sign-in"
fi
ok "daemon $(api http://x/api/version | jq -r .version), dashboard over HTTP and at https://homelab.$domain$signed$wol"

fixedids && ok "caddy, homelab-daemon and homelab-nixd have their fixed ids"

step "5. a bcachefs pool through the daemon"
vssh 'bcachefs format -q --replicas=2 /dev/vdb /dev/vdc >/dev/null'
# Pools mount under /var/mnt, by name. A release from before that (0.2.5,
# the first version of a check that updates from it) has /pool in its image
# and takes a path.
pool=/var/mnt/pool guarded=
if has "pools mounted under /var/mnt" vssh 'test -L /mnt'; then
  ask POST storage/mount '{"devices":["/dev/vdb","/dev/vdc"]}' || die "mount refused"
  vssh "findmnt -no FSTYPE $pool" | seen bcachefs || die "$pool is not mounted"
  vssh 'touch /mnt/pool/.check && rm /var/mnt/pool/.check' || die "/mnt/pool is not the pool"
  code=$(code POST storage/mount '{"devices":["/dev/vdb"],"name":"../x"}')
  [ "$code" = 422 ] || [ "$code" = 400 ] || die "a pool name with a path in it answered $code"
  # Unmounted, its directory takes no writes: they would land on /var.
  ask POST storage/unmount "{\"mountpoint\":\"$pool\"}" || die "unmount refused"
  vssh "! touch $pool/x 2>/dev/null" || die "the unmounted $pool can be written to"
  ask POST storage/mount '{"devices":["/dev/vdb","/dev/vdc"]}' || die "the second mount was refused"
  vssh "findmnt -no FSTYPE $pool" | seen bcachefs || die "$pool is not mounted again under the name it had"
  guarded="; unmounted, its directory takes no writes"
else
  pool=/pool
  ask POST storage/mount '{"devices":["/dev/vdb","/dev/vdc"],"mountpoint":"/pool"}' || die "mount refused"
  vssh 'findmnt -no FSTYPE /pool' | seen bcachefs || die "/pool is not mounted"
fi
ok "pool mounted at $pool ($(vssh 'bcachefs version'))$guarded"

# A partition: a file system that is not a pool (vdd, formatted here). Only
# it is offered, not the ESP, the root slots, /var or the pool's disks; it
# mounts under its label's name, and is remembered for the next boot.
part=
if has "a partition that is not a pool, mounted from the API" apihas storage 'has("partitions")'; then
  vssh 'mkfs.ext4 -q -L "Check Data" /dev/vdd'
  offered() { st=$(api http://x/api/storage) && [ "$(jq -r '[.partitions[].path] | join(" ")' <<<"$st")" = /dev/vdd ]; }
  wait_for 30 offered || true
  [ "$(jq -r '[.partitions[] | "\(.path) \(.fstype) \(.state) \(.mount_name)"] | join(", ")' <<<"$st")" = "/dev/vdd ext4 unmounted check-data" ] ||
    die "the partitions offered are not just vdd: $(jq -c .partitions <<<"$st")"
  partuuid=$(jq -r '.partitions[0].uuid' <<<"$st")
  part=/var/mnt/check-data
  ask POST "storage/partitions/$partuuid/mount" '{}' || die "the partition's mount was refused"
  vssh "findmnt -no FSTYPE,OPTIONS $part" | seen '^ext4 .*nosuid' || die "$part is not mounted: $(vssh "findmnt -no FSTYPE,OPTIONS $part")"
  code=$(code POST "storage/partitions/$partuuid/mount" '{"name":"pool"}')
  [ "$code" = 409 ] || die "mounting a mounted partition answered $code"
  ask POST "storage/partitions/$partuuid/unmount" || die "the partition's unmount was refused"
  vssh "! touch $part/x 2>/dev/null" || die "the unmounted $part can be written to"
  ask POST "storage/partitions/$partuuid/mount" '{}' || die "the partition's second mount was refused"
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
ask POST stacks '{"name":"site","source":"git+file:///var/lib/stack-src/site"}' || die "install refused"
# The stack's page makes the folder: a subvolume on the pool, owned by the
# host's ids for the user and group the template declares (nginx, the first
# name, so 2000). A version from before that takes a folder made by hand.
madefolder=
if has "a stack's folder made from the API" answers 200 GET stacks-ids; then
  ask POST stacks/site/variables/SITE_DIR/folder "{\"place\":\"$pool\",\"folder\":\"site\"}" || die "the folder was not made"
  [ "$(vssh "stat -c %u:%g $pool/site")" = 2000:2000 ] || die "$pool/site is owned by $(vssh "stat -c %u:%g $pool/site"), not the stack ids 2000:2000"
  vssh "bcachefs subvolume list $pool" | seen -w site || die "$pool/site is not a subvolume"
  [ "$(api http://x/api/stacks/site | jq -r '.variables[] | select(.name == "SITE_DIR") | "\(.value) \(.owner_state)"')" = "$pool/site ok" ] || die "SITE_DIR is not the made folder on the stack ids"
  madefolder="; its folder a subvolume made from the API, owned by the stack ids"
else
  vssh "mkdir -p $pool/site && chown 2000:2000 $pool/site"
  put stacks/site/variables/SITE_DIR "{\"value\":\"$pool/site\"}" || die "variable refused"
fi
vssh "echo '<p>image-check</p>' > $pool/site/index.html"
# A folder bound on top of what the manifest asks for, read-only. A version
# from before added folders has none.
added=
if has "a host folder added to a stack" apihas stacks/site 'has("folders")'; then
  vssh "mkdir -p $pool/library && echo shared > $pool/library/file"
  put stacks/site/folders "{\"folders\":[{\"path\":\"$pool/library\",\"mount\":\"/library\",\"read_only\":true}]}" ||
    die "the added folder was refused"
  added=1
fi
ask POST stacks/site/approve "{\"rev\":\"$rev\"}" || die "approve refused"
ask POST stacks/site/start || die "start refused"
# Nix builds the stack: the VM is busy, and one poll that fails is not the
# stack's failure.
s=''
for _ in $(seq 1 120); do
  sleep 5
  s=$(api http://x/api/stacks/site) || continue
  [ "$(jq -r .running <<<"$s")" != true ] || break
  ! jq -e '.operation.error // empty' <<<"$s" >/dev/null || die "stack failed: $(jq -r .operation.error <<<"$s")"
done
[ "$(jq -r .running <<<"$s")" = true ] || die "the stack did not start in 10 minutes"
https site / | seen image-check || die "the stack's route is not served at site.$domain"
refused=""
if has "the dashboard's sign-in" hassignin; then
  stackrefused site
  refused="; the dashboard refuses it"
fi
if [ -n "$onvar" ]; then
  st=$(api http://x/api/stacks-store)
  jq -e '.total_bytes > 0 and .free_bytes > 0 and (.own_partition | not)' <<<"$st" >/dev/null || die "the Nix store's disk is not reported: $st"
fi
[ -z "$madefolder" ] || vssh 'cat /var/lib/homelab/appdata/stacks/site/root/etc/passwd' | seen '^nginx:x:2000:' || die "nginx in the stack does not have the id 2000"
if [ -n "$added" ]; then
  leader=$(vssh "machinectl show site -p Leader --value")
  # Through the leader's root: the stack's mounts, read-only flag and all,
  # without a binary of the host's inside the stack.
  [ "$(vssh "cat /proc/$leader/root/library/file")" = shared ] || die "the added folder is not at /library in the stack"
  vssh "! touch /proc/$leader/root/library/new 2>/dev/null" || die "the stack wrote to the read-only added folder"
  vssh "grep -q ' /library ro,' /proc/$leader/mountinfo" || die "/library is not mounted read-only in the stack"
  added="; a host folder added read-only at /library"
fi
ok "stack site built, serving the page from the pool at https://site.$domain$refused$madefolder$added"
stackfork "$rev" || true
https site / | seen image-check || die "the stack's route is not served after the fork"
stackfreeze && ok "stack site frozen with its processes kept, its route silent and the API answering; thawed, the same processes serve it"
pool_lifecycle

step "7. host settings: VPN, tunnel, firewall, name and time zone"
peer=$(vssh 'wg genkey | wg pubkey')
put host-settings/wireguard "$(jq -n --arg k "$peer" '{enabled: true, address: "10.10.0.1/24", endpoint: "vpn.example.net:51820",
  peers: [{name: "laptop", public_key: $k, address: "10.10.0.2"}]}')" || die "VPN refused"
if has "a VPN device's keys made by the host" hasdevicekeys; then devicekeys; fi
creds='{"AccountTag":"acc","TunnelID":"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0","TunnelSecret":"c2VjcmV0"}'
put host-settings/tunnel "$(jq -n --arg c "$creds" '{enabled: true, credentials: $c, routes: [{hostname: "site.example.net", target: "site"}]}')" || die "tunnel refused"
put host-settings/firewall '{"openings":[{"port":8443,"proto":"tcp","from":["10.0.2.0/24"]}]}' || die "firewall refused"
put host-settings/host '{"hostname":"checkhost","timezone":"Europe/Stockholm"}' || die "host refused"
errs=$(api http://x/api/host-settings | jq -r '.applied.errors | join("; ")')
[ -z "$errs" ] || die "not applied: $errs"
sleep 2
hostcheck
ok "VPN devices$made, tunnel route, firewall, host name and time zone in place"

step "8. after a reboot"
froze=""
if has "freezing a stack" apihas stacks/site 'has("frozen")'; then
  post stacks/site/freeze '{}' >/dev/null || die "the freeze before the reboot was refused"
  [ "$(post system/power '{"action":"reboot"}' | jq -r .code)" = stacks_frozen ] ||
    die "a reboot from the API was not refused with the stack frozen"
  froze="; frozen before it, thawed by it and started at boot"
fi
reboot_vm
booted "$version"
hostcheck
[ -z "$part" ] || [ "$(vssh "cat $part/file")" = kept ] || die "the partition is not mounted at $part after a reboot"
[ "$(vssh 'cat /etc/machine-id')" = "$installed" ] || die "the machine ID changed over a reboot"
stackup 0 "at the end of the step"
[ -z "$froze" ] || [ "$(api http://x/api/stacks/site | jq -r '"\(.frozen) \(.autostart)"')" = "false true" ] ||
  die "the stack after the reboot: $(api http://x/api/stacks/site | jq -c '{frozen, autostart}')"
ok "everything still in place, the machine ID too (the stack running ${sitewait}s after the daemon answered$froze)"

if [ -n "$next" ]; then
  step "9. $next downloads from a release server, then a core update to it through the API"
  rm -rf "$vm/gnupg" && mkdir -m 700 "$vm/gnupg"
  gpg --homedir "$vm/gnupg" --batch --quiet --passphrase '' --quick-gen-key 'image check <check@example.com>' ed25519 sign never
  gpg --homedir "$vm/gnupg" --armor --export > "$vm/release.asc"
  mkdir -p "$vm/release"
  python3 -m http.server --bind 127.0.0.1 --directory "$vm/release" "$relport" >"$vm/release.log" 2>&1 9>&- &
  relpid=$!
  wait_for 30 listening "$relport" || die "the release server did not start: $(tail -n 3 "$vm/release.log" | tr '\n' ';')"
  vscp "$vm/release.asc" root@127.0.0.1:/var/lib/homelab/release.asc
  vssh "printf 'HOMELAB_RELEASE_URL=http://10.0.2.2:$relport\nHOMELAB_RELEASE_KEYRING=/var/lib/homelab/release.asc\n' > /var/lib/homelab/daemon.env && systemctl restart homelab-daemon"
  daemon || die "the daemon does not answer with the release server set"
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
  # A core update restarts Caddy too: an older version's open API is gated
  # now. The core is $next's from here on, and has what $next has.
  running=$next
  signed=""
  if has "the dashboard's sign-in" hassignin; then
    signin
    stackrefused site
    signed="; the dashboard behind its sign-in, refused to the stack"
  fi
  running=$version
  ok "core $next active, no reboot, the stack's container untouched$signed"

  step "10. an image update to $next through the API"
  mark
  op system-updates/image/install '{"reboot":"now"}' >/dev/null || true
  # The install reboots the host: wait for the next boot.
  newboot
  daemon 240 || die "the daemon does not answer after the update to $next"
  booted "$next"
  vssh 'systemd-sysext status --no-legend' | seen homelab-core || die "the core extension is not merged on $next"
  fixedids || true
  # $next has no /pool: a pool the first version mounted there is mounted
  # again by hand, under its name, and the stack is given its new path.
  if [ "$pool" = /pool ] && has "pools mounted under /var/mnt" vssh 'test -L /mnt'; then
    pool=/var/mnt/pool
    ask POST storage/mount '{"devices":["/dev/vdb","/dev/vdc"]}' || die "mount refused on $next"
    put stacks/site/variables/SITE_DIR "{\"value\":\"$pool/site\"}" || die "variable refused on $next"
    send POST stacks/site/stop >/dev/null 2>&1 || true
    wait_for 90 stackis '.operation.running // false' false || die "the stack's stop did not end on $next"
    ask POST stacks/site/start || die "start refused on $next"
  fi
  hostcheck
  # A release before the VPN device's keys left that part of step 7 to $next.
  later=
  if [ "$vpndevices" = 1 ]; then
    hasdevicekeys || die "$next does not make a VPN device's keys"
    devicekeys
    hostcheck
    later=", and a VPN device with keys it made"
  fi
  stackrace
  ok "booted $next with everything in place$later; the stack's link kept its address through network reloads"
  # The way back by hand: the version before is still in its slot.
  st=$(post system-updates/image/boot "{\"version\":\"$version\",\"mode\":\"default\"}") || die "making $version the default was refused: $st"
  [ "$(jq -r .default <<<"$st")" = "$version" ] || die "$version is not the default: $(jq -c '{default, oneshot}' <<<"$st")"
  st=$(post system-updates/image/boot '{"version":"","mode":"default"}') || die "clearing the default was refused: $st"
  [ "$(jq -r '.default // ""' <<<"$st")" = "" ] || die "the default is still set: $(jq -c '{default, oneshot}' <<<"$st")"
  [ "$(post system-updates/image/boot '{"version":"0.0.0.1","mode":"once"}' | jq -r .code)" = no_such_version ] || die "a version that is not installed was accepted"
  mark
  st=$(post system-updates/image/boot "{\"version\":\"$version\",\"mode\":\"once\",\"reboot\":true}") || die "booting $version once was refused: $st"
  [ "$(jq -r .oneshot <<<"$st")" = "$version" ] || die "$version is not set to boot once: $(jq -c '{default, oneshot}' <<<"$st")"
  newboot
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
  if has "a downloaded core in use after a restart" vssh 'test -x /usr/lib/homelab/core-at-boot'; then
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
    nofailed
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
  # Three failed tries of the broken version, then the one before.
  restart 1800
  daemon 240 || die "the daemon does not answer after the fall back"
  booted "$next"
  [ "$(api http://x/api/system-updates/image | jq -r .fell_back_from)" = "$broken" ] || die "the daemon does not say it fell back from $broken"
  hostcheck
  ok "back on $next by itself; the daemon reports the fall back from $broken"

  # A daemon that cannot start (a file where its state folder was), on a
  # version that was good before: the boot check tries the core before,
  # fails, and leaves the host up.
  vssh 'systemctl stop homelab-daemon && mv /var/lib/homelab-daemon /var/lib/homelab-daemon.keep && touch /var/lib/homelab-daemon'
  restart
  bootid=$(vssh 'cat /proc/sys/kernel/random/boot_id')
  wait_for 900 is failed vssh 'systemctl is-failed homelab-boot-check.service' || true
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
  restart
  daemon 600 || die "the daemon does not answer on the core before"
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
  # With the tags: build.sh versions the build above the newest release
  # (<tag>.<commit time>). Without them it is 0.0.<commit time>, older than
  # the installed version, and staging prunes it. The versions in image/out
  # must be older than HEAD's commit time for the build to be newer.
  git -C "$root" bundle create "$vm/src.bundle" HEAD --tags 2>/dev/null
  vscp "$vm/src.bundle" root@127.0.0.1:/var/lib/homelab/src.bundle
  vssh 'echo HOMELAB_CORE_SOURCE=git+file:///var/lib/homelab/src.bundle > /var/lib/homelab/daemon.env && systemctl restart homelab-daemon'
  daemon || die "the daemon does not answer with its source set"
  # The memory the whole host uses while it builds: what a host that builds
  # its own updates needs.
  vssh 'cat > /var/tmp/memwatch.sh' <<'EOS'
while :; do free -m | awk '/^Mem/ {print $3}' >> /var/tmp/mem.log; sleep 2; done
EOS
  vssh 'systemd-run -q --unit=memwatch sh /var/tmp/memwatch.sh'
  # Twice: the second build reuses mkosi's packages and OS tree. Neither
  # copies mkosi's outputs across mounts.
  times=
  for n in 1 2; do
    since=$(vssh 'date +%s')
    start=$SECONDS
    d=$(OPWAIT=720 op system-updates/image/build)
    [ "$(jq -r .result <<<"$d")" = healthy ] || die "build $n: $(jq -r '.result + ": " + .reason' <<<"$d")"
    built=$(jq -r .version <<<"$d")
    api http://x/api/system-updates/image | jq -e --arg v "$built" '.core.available + .available | index($v)' >/dev/null || die "$built is not staged"
    # systemd logs the unit's memory peak when it stops.
    vssh 'for _ in $(seq 1 30); do systemctl is-active -q homelab-core-deploy || break; sleep 1; done'
    journal=$(vssh "journalctl -q -o cat -u homelab-core-deploy --since=@$since")
    # Saving a new incremental cache copies once (/out to /cache); the
    # outputs are renamed into /out.
    ! grep 'falling back to copying' <<<"$journal" | grep -q ' to /out/' || die "build $n copied mkosi's outputs: $(grep -m1 'falling back to copying' <<<"$journal")"
    [ "$n" = 1 ] || ! grep -q 'Retrieving packages' <<<"$journal" || die "the second build downloaded packages again"
    times+="build $n: $((SECONDS - start)) s, $(grep -c 'Retrieving packages' <<<"$journal") package downloads, unit $(grep -o '[0-9.]*[KMG] memory peak' <<<"$journal" | tail -1 || echo 'peak unknown'); "
  done
  vssh 'systemctl stop memwatch'
  ok "built and staged $built twice (${times%; }); memory in use peaked at $(vssh 'sort -n /var/tmp/mem.log | tail -1') of $mem MiB"
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
vssh 'lsblk -no PARTTYPENAME /dev/vda' | seen "Microsoft basic data" || die "the other system's partition is gone"
vssh 'bootctl list' | seen 'id: auto-windows' || die "the boot menu has no Windows entry"
vssh 'grep -q "^Address=10.0.2.15/24" /etc/systemd/network/10-homelab-wired.network' || die "the installer's static address is not set"
vssh 'ip -4 -o addr show' | seen 'inet 10.0.2.15/24 .* global' || die "the static address is not on the interface"
vssh 'bootctl list' | grep -B1 'id: homelab_' | seen '(default)' || die "the system is not the boot menu's default"
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
if has "the Nix store on /var" storeonvar; then onvar=1; fi
[ "$(vssh cat /proc/sys/kernel/hostname)" = crossdisk ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname)"
vssh 'ls /boot/EFI/Linux/ >/dev/null; findmnt -no SOURCE /boot' | seen '^/dev/vdb1$' || die "/boot is not the other disk's ESP: $(vssh 'findmnt -no SOURCE /boot')"
vssh 'ls /boot/EFI/Linux/' | seen -Fx "homelab_$under.efi" || die "the kernel is not on the other disk's ESP"
parts=$(vssh 'lsblk -no PARTTYPENAME /dev/vda') || die "could not list the system disk's partitions"
! seen "EFI System" <<<"$parts" || die "the system disk got an ESP of its own"
vssh 'bootctl list' | seen 'id: auto-windows' || die "the boot menu has no Windows entry"
vssh 'bootctl list' | grep -B1 'id: homelab_' | seen '(default)' || die "the system is not the boot menu's default"
[ "$(espfiles /dev/vdb1)" = "WINDOWS-BOOTMGR WINDOWS-FALLBACK " ] || die "the other system's loaders changed: $(espfiles /dev/vdb1)"
ok "boots from the other disk's ESP, mounted at /boot; Windows in its menu, its loaders untouched"

step "15. that boot had the stick left in"
[ -n "$(stickname)" ] || die "the stick is not among the VM's disks"
daemon || die "the daemon does not answer with the stick in"
[ "$(api http://x/api/installer | jq -r .active)" = false ] || die "the installed host runs the installer with the stick in"
[ "$(vssh cat /proc/sys/kernel/hostname)" = crossdisk ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname)"
[ "$(vssh 'cat /etc/machine-id')" = "$crossdisk" ] || die "the machine ID changed with the stick in"
vssh 'ls /boot/EFI/Linux/ >/dev/null; findmnt -no SOURCE /boot' | seen '^/dev/vdb1$' || die "/boot is not the other disk's ESP: $(vssh 'findmnt -no SOURCE /boot')"
nologin "with the stick in"
nofailed
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
mark
send POST installer/restart '' -H "'X-Installer-Token: $token'" >/dev/null || true
# The same VM comes back by itself, the stick still first in the order the
# firmware was told.
if ! wait_for 480 otherboot; then
  vmalive || die "the VM turned off instead of restarting"
  die "the machine did not restart (serial log $serial)"
fi
booted "$under"
daemon || die "the daemon does not answer after the restart"
[ "$(api http://x/api/installer | jq -r .active)" = false ] || die "the restart started the installer again, not the new system"
[ -n "$(stickname)" ] || die "the stick is not among the VM's disks"
runs_from vda
[ "$(vssh cat /proc/sys/kernel/hostname)" = again ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname)"
nologin "with the stick in"
nofailed
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
storagedrivers() { inuki "$under" scsi/vmw_pvscsi && inuki "$under" scsi/megaraid/megaraid_sas && inuki "$under" usb/storage/uas; }
if has "the storage drivers the image names, in the initrd" storagedrivers; then
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
nofailed
ok "the same on the next boot$drivers; $(wc -w <<<"$ids") installs, each with its own"

step "18. the system boots with its disk behind other controllers"
if [ -n "$drivers" ]; then
  for c in megasas uas; do
    stop
    ctl "$c"
    [ "$(vssh 'cat /etc/machine-id')" = "$id" ] || die "behind $c another system booted"
    nofailed "behind $c"
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
  nofailed
  ok "/nix is the disk's nix partition ($nixpart)"
else
  ok "skipped: $under has the store on a partition of its own already"
fi

# A run that passed needs none of its disks.
stop
drop sys esp
printf '  step %s took %s\n' "$stepname" "$(took "$stepstart")"
printf '\nimage-check passed in %s: %s%s%s (the disks of the VM took at most %d MB)\n' "$(took 0)" "$version" "${next:+ → $next}" "${broken:+, fell back from $broken}" "$peak"
[ -z "$skipped" ] || printf 'not checked, on a version from before it: %s\n' "$skipped"
