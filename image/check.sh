#!/usr/bin/env bash
# check.sh: boot the image `make image` built (image/out) in a VM and check
# it end to end. The disk image is the installer medium (an 8 GB stick):
#   1. it stays under 1.5 GB, boots on its verity root into installer mode and
#      lists the disks
#   2. it installs into the free region picked on a stand-in Windows disk (an
#      ESP with another system's loaders, a gap, an NTFS partition), leaves
#      the gap alone and turns off
#   3. it erases a blank disk and installs onto it
#   4. that disk boots alone with the core merged and the installer's host
#      name; a domain set through the host settings serves the dashboard over
#      HTTPS
#   5. a bcachefs pool is formatted and mounted through the daemon
#   6. a stack (the drafts template) is built by Nix and serves a page from
#      the pool at its route under the domain
#   7. host settings: a VPN device, a tunnel route, the firewall, host name
#      and time zone take effect without a rebuild
#   8. all of it holds after a reboot
#   9. the next version is published on a release server (signed with a key
#      of the check's own): a copy whose SHA256SUMS changed after signing is
#      refused, the real one downloads and is staged; then a core update to
#      it through the API, without a reboot, the stack untouched
#  10. an image update to the downloaded version through the API, with a
#      reboot; everything holds
#  11. a broken image (a third version, its root corrupted on purpose) falls
#      back to the one before by itself, and the daemon says so
#  12. with BUILD=1: the host builds an update from main itself
#  13. the free-space install boots alone, next to the other system's files
#  14. an erased disk whose boot loader goes on another disk's ESP boots
#      with that ESP at /boot and Windows in its boot menu
# The firmware's variables last the whole run, as on a real machine, so the
# boot entries the installs register are used.
# Steps 9-10 need two versions in image/out and step 11 three:
#   make image VERSION=0.0.1 && make image VERSION=0.0.2 && make image VERSION=0.0.3
# Run inside the image shell (`make image-check`). Leaves image/out/vm;
# KEEP=1 leaves the VM running when a step fails. CPUS and MEM (MiB) size the
# VM: by default a small host's 4 CPUs and 4 GB, 6 GB with BUILD=1.
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
[ -e "$out/homelab_$version.raw" ] && [ -e "$core" ] || { echo "image $version is incomplete in $out: run make image again" >&2; exit 1; }
domain=check.example.com

# step "N. what": a step's heading; scripts/run reads the progress line.
steps=14
step() { printf '\nprogress: %d%%\n▸ %s\n' "$(((${1%%.*} - 1) * 100 / steps))" "$*"; }
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
# windisk FILE [GAP]: a stand-in for a Windows disk: a 512 MB ESP with Windows
# Boot Manager and a fallback loader of its own, then GAP free (as Windows
# leaves after shrinking or deleting a partition), an NTFS-typed partition,
# and the rest free.
windisk() {
  qemu-img create -q -f raw "$1" 90G
  if [ -n "${2:-}" ]; then
    printf 'label: gpt\n,512M,U\n,%s,L\n,20G,EBD0A0A2-B9E5-4433-87C0-68B6B72699C7\n' "$2" | sfdisk -q "$1"
    sfdisk -q --delete "$1" 2
  else
    printf 'label: gpt\n,512M,U\n,20G,EBD0A0A2-B9E5-4433-87C0-68B6B72699C7\n' | sfdisk -q "$1"
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
windisk "$vm/win2.raw"
cpus=${CPUS:-4}
# A build on the host (BUILD=1): one Go compile takes about 2.5 GB.
mem=${MEM:-$([ "${BUILD:-}" = 1 ] && echo 6144 || echo 4096)}
sshport=$(shuf -i 20000-29999 -n1)
httpport=$((sshport + 1))
# The release server step 9 runs; the VM reaches it at 10.0.2.2.
relport=$((sshport + 2))

# boot DISK [EXTRA...]: start the VM headless with root's SSH key as a
# systemd credential, the pool disks, and DISK first.
boot() {
  local first=$1
  shift
  if [ ! -e "$vm/vars.fd" ]; then
    cp "$HOMELAB_OVMF/OVMF_VARS.fd" "$vm/vars.fd"
    chmod 644 "$vm/vars.fd"
  fi
  local extra=()
  for d in "$@"; do extra+=(-drive "if=virtio,format=raw,file=$d,cache=unsafe"); done
  qemu-system-x86_64 -machine q35,accel=kvm -cpu host -smp "$cpus" -m "$mem" \
    -uuid 9a0c1e2f-3b4d-4e5f-8a9b-0c1d2e3f4a5b \
    -drive "if=pflash,format=raw,readonly=on,file=$HOMELAB_OVMF/OVMF_CODE.fd" \
    -drive "if=pflash,format=raw,file=$vm/vars.fd" \
    -drive "if=virtio,format=raw,file=$first,cache=unsafe" "${extra[@]}" \
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
# console_users: who is logged in on the console (a login session, not SSH).
console_users() {
  vssh 'for s in $(loginctl list-sessions --no-legend | awk "{print \$1}"); do
    [ "$(loginctl show-session "$s" -p Service --value)" = login ] && loginctl show-session "$s" -p Name --value; done; true'
}
# put PATH JSON: PUT a body to the daemon's API, the body over stdin.
put() { vssh "cat > /tmp/body.json" <<<"$2" && api -X PUT "http://x/api/$1" -d @/tmp/body.json >/dev/null; }
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

# hostcheck: everything the host settings asked for is in place.
hostcheck() {
  https homelab /api/auth >/dev/null || die "the dashboard is not served at homelab.$domain"
  https site / | grep -q image-check || die "the stack's route is not served at site.$domain"
  vssh 'wg show wg-homelab peers' | grep -q . || die "the VPN has no device"
  vssh 'ip -4 -o addr show wg-homelab' | grep -q 10.10.0.1/24 || die "the VPN interface has no address"
  vssh 'grep -q "originServerName: site.'"$domain"'" /var/lib/homelab/cloudflared/config.yml' || die "the tunnel route is not in cloudflared's config"
  vssh 'systemctl is-enabled homelab-cloudflared' | grep -q runtime || die "the tunnel is not turned on"
  vssh 'nft list table inet homelab_host' | grep -q 'dport 8443 accept' || die "the firewall table is not loaded"
  [ "$(vssh cat /proc/sys/kernel/hostname)" = checkhost ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname)"
  vssh 'readlink /etc/localtime' | grep -q Europe/Stockholm || die "the time zone is not set"
  [ "$(api http://x/api/stacks/site | jq -r .running)" = true ] || die "the stack is not running"
}

# install DISK ERASE HOSTNAME [IPV4] [ESP] [REGION]: install from the medium
# through the installer's API (into the free region starting at byte REGION),
# then turn the machine off from the page's button.
install() {
  local token st
  token=$(api -X POST http://x/api/installer/claim | jq -r .token) || die "claim refused"
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
  api -X POST -H "'X-Installer-Token: $token'" http://x/api/installer/poweroff >/dev/null || true
  off
}
# espfiles DEV: the other system's files on an ESP, as one line; read where
# the ESP is mounted, else from a mount of its own.
espfiles() {
  vssh "d=\$(findmnt -no TARGET $1 | head -1); if [ -z \"\$d\" ]; then mkdir -p /run/esp && mount -o ro $1 /run/esp && d=/run/esp; fi
    cat \$d/EFI/Microsoft/Boot/bootmgfw.efi \$d/EFI/BOOT/BOOTX64.EFI | tr '\\n' ' '; [ \$d != /run/esp ] || umount /run/esp"
}

step "1. the installer medium boots $version into installer mode"
size=$(stat -c %s "$out/homelab_$version.raw")
# 1.26 GB in November 2026; the limit catches it growing back.
[ "$size" -le 1500000000 ] || die "the installer medium is $size bytes: it should stay under 1.5 GB"
boot "$vm/stick.raw" "$vm/disk.raw" "$vm/beside.raw"
booted "$version"
vssh 'systemctl --failed --no-legend' | grep . && die "failed units" || true
for _ in $(seq 1 30); do api http://x/api/installer >/dev/null 2>&1 && break; sleep 2; done
st=$(api http://x/api/installer) || die "the installer does not answer"
jq -e '.active and (.disks[] | select(.name == "vda") | .in_use) and (.disks[] | select(.name == "vdb") | .erase.ok)
  and (.disks[] | select(.name == "vdc") | .beside.ok)' <<<"$st" >/dev/null || die "unexpected disks: $(jq -c '[.disks[] | {name, in_use, erase, beside}]' <<<"$st")"
curl -sf "http://127.0.0.1:$httpport/" | grep -q '<html' || die "the dashboard does not answer over HTTP"
for _ in $(seq 1 15); do console_users | grep -qx root && break; sleep 2; done
console_users | grep -qx root || die "the installer's console does not log root in"
ok "$((size / 1000000)) MB; verity root, boot-complete, no failed units; the medium in use, a blank disk and free space beside Windows offered; root logged in on the console"

step "2. install into the free space next to the other system"
# The large free region after the NTFS partition, not the 2 GB gap before it.
region=$(jq -r '.disks[] | select(.name == "vdc") | .free | max_by(.size) | .start' <<<"$st")
install vdc false beside '{"mode":"static","address":"10.0.2.15/24","gateway":"10.0.2.2","dns":["10.0.2.3"]}' "" "$region"
ntfs=$(sfdisk -J "$vm/beside.raw" | jq '.partitiontable.partitions[] | select(.type == "EBD0A0A2-B9E5-4433-87C0-68B6B72699C7") | .start')
ingap=$(sfdisk -J "$vm/beside.raw" | jq --argjson n "$ntfs" '[.partitiontable.partitions[] | select(.start > 2048 and .start < $n)] | length')
[ "$ingap" = 0 ] || die "$ingap new partitions went into the gap before the other system's: $(sfdisk -d "$vm/beside.raw")"
sfdisk -d "$vm/beside.raw" | grep -q homelab-reserved && die "a temporary partition was left on the disk"
ok "installed into vdc's selected free region (not the gap before the other system) and turned off"

step "3. erase a disk and install"
# Without the first install: its root has the stick's partition UUIDs.
boot "$vm/stick.raw" "$vm/disk.raw"
install vdb true installed
ok "installed onto vdb and turned off"

step "4. the installed disk boots $version with its core, its name, time zone, SSH key and a domain"
boot "$vm/disk.raw" "$vm/pool1.raw" "$vm/pool2.raw"
booted "$version"
vssh 'test "$(findmnt -no SIZE /var | tr -d G | cut -d. -f1)" -gt 30' || die "/var does not fill the disk"
vssh 'systemd-sysext status --no-legend' | grep -q homelab-core || die "the core extension is not merged"
for _ in $(seq 1 30); do api http://x/api/version >/dev/null 2>&1 && break; sleep 2; done
api http://x/api/version >/dev/null || die "the daemon does not answer"
[ "$(api http://x/api/installer | jq -r .active)" = false ] || die "the installed host runs the installer"
console_users | grep -q . && die "the installed host logs $(console_users | tr '\n' ' ')in on the console" || true
[ "$(vssh cat /proc/sys/kernel/hostname)" = installed ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname), not the installer's"
[ "$(api http://x/api/host-settings | jq -r .host.hostname)" = installed ] || die "the installer's host name is not in the settings"
vssh 'readlink /etc/localtime' | grep -q Europe/Stockholm || die "the installer's time zone is not set"
ssh -q -i "$vm/key2" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -p "$sshport" root@127.0.0.1 true ||
  die "the SSH key given to the installer does not let root in"
vssh 'systemctl --failed --no-legend' | grep . && die "failed units" || true
curl -sf "http://127.0.0.1:$httpport/" | grep -q '<html' || die "the dashboard does not answer over HTTP"
[ "$(api http://x/api/host-settings | jq -r .managed)" = image ] || die "the daemon does not see an image host"
put host-settings/domain "{\"domain\":\"$domain\"}" || die "domain refused"
for _ in $(seq 1 15); do https homelab /api/auth >/dev/null 2>&1 && break; sleep 2; done
https homelab /api/auth >/dev/null || die "the dashboard is not served at homelab.$domain"
signin
ok "daemon $(api http://x/api/version | jq -r .version), dashboard over HTTP and at https://homelab.$domain behind its sign-in"

step "5. a bcachefs pool through the daemon"
vssh 'bcachefs format -q --replicas=2 /dev/vdb /dev/vdc >/dev/null'
api -X POST http://x/api/storage/mount -d "'{\"devices\":[\"/dev/vdb\",\"/dev/vdc\"],\"mountpoint\":\"/pool\"}'" >/dev/null || die "mount refused"
vssh 'findmnt -no FSTYPE /pool' | grep -q bcachefs || die "/pool is not mounted"
ok "pool mounted at /pool ($(vssh 'bcachefs version'))"

step "6. a stack from the drafts template"
vssh 'mkdir -p /pool/site /var/lib/stack-src/site && echo "<p>image-check</p>" > /pool/site/index.html'
for f in homelab-stack.json flake.nix; do
  sed 's/NAME/site/g' "$root/apps/core/internal/drafts/template/$f" | vssh "cat > /var/lib/stack-src/site/$f"
done
vssh 'cd /var/lib/stack-src/site && git init -q && git add -A && git -c user.name=check -c user.email=check@example.com commit -qm site'
rev=$(vssh 'git -C /var/lib/stack-src/site rev-parse HEAD')
api -X POST http://x/api/stacks -d "'{\"name\":\"site\",\"source\":\"git+file:///var/lib/stack-src/site\"}'" >/dev/null || die "install refused"
api -X PUT http://x/api/stacks/site/variables/SITE_DIR -d "'{\"value\":\"/pool/site\"}'" >/dev/null || die "variable refused"
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
stackrefused site
ok "stack site built, serving the page from the pool at https://site.$domain; the dashboard refuses it"

step "7. host settings: VPN, tunnel, firewall, name and time zone"
peer=$(vssh 'wg genkey | wg pubkey')
put host-settings/wireguard "$(jq -n --arg k "$peer" '{enabled: true, address: "10.10.0.1/24", endpoint: "vpn.example.net:51820",
  peers: [{name: "laptop", public_key: $k, address: "10.10.0.2"}]}')" || die "VPN refused"
creds='{"AccountTag":"acc","TunnelID":"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0","TunnelSecret":"c2VjcmV0"}'
put host-settings/tunnel "$(jq -n --arg c "$creds" '{enabled: true, credentials: $c, routes: [{hostname: "site.example.net", target: "site"}]}')" || die "tunnel refused"
put host-settings/firewall '{"openings":[{"port":8443,"proto":"tcp","from":["10.0.2.0/24"]}]}' || die "firewall refused"
put host-settings/host '{"hostname":"checkhost","timezone":"Europe/Stockholm"}' || die "host refused"
errs=$(api http://x/api/host-settings | jq -r '.applied.errors | join("; ")')
[ -z "$errs" ] || die "not applied: $errs"
sleep 2
hostcheck
ok "VPN device, tunnel route, firewall, host name and time zone in place"

step "8. after a reboot"
reboot_vm
booted "$version"
hostcheck
ok "everything still in place"

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
  ok "core $next active, no reboot, the stack's container untouched"

  step "10. an image update to $next through the API"
  op system-updates/image/install '{"reboot":"now"}' >/dev/null || true
  # The install reboots the host: wait for it to go, then come back.
  for _ in $(seq 1 60); do vssh true 2>/dev/null || break; sleep 2; done
  up
  for _ in $(seq 1 30); do api http://x/api/version >/dev/null 2>&1 && break; sleep 2; done
  booted "$next"
  vssh 'systemd-sysext status --no-legend' | grep -q homelab-core || die "the core extension is not merged on $next"
  hostcheck
  ok "booted $next with everything in place"
else
  step "9-10. core and image updates skipped: image/out has one version"
fi

if [ -n "$broken" ]; then
  step "11. a broken image ($broken) falls back to $next"
  stage_image "$broken"
  d=$(op system-updates/image/install)
  [ "$(jq -r .version <<<"$d")" = "$broken" ] || die "$broken was not installed: $d"
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
boot "$vm/beside.raw"
booted "$version"
[ "$(vssh cat /proc/sys/kernel/hostname)" = beside ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname)"
[ "$(espfiles /dev/vda1)" = "WINDOWS-BOOTMGR WINDOWS-FALLBACK " ] || die "the other system's loaders changed: $(espfiles /dev/vda1)"
vssh 'lsblk -no PARTTYPENAME /dev/vda' | grep -q "Microsoft basic data" || die "the other system's partition is gone"
vssh 'bootctl list' | grep -q 'id: auto-windows' || die "the boot menu has no Windows entry"
vssh 'grep -q "^Address=10.0.2.15/24" /etc/systemd/network/10-homelab-wired.network' || die "the installer's static address is not set"
vssh 'ip -4 -o addr show' | grep -q 'inet 10.0.2.15/24 .* global' || die "the static address is not on the interface"
vssh 'bootctl list' | grep -B1 'id: homelab_' | grep -q '(default)' || die "the system is not the boot menu's default"
ok "boots alone with its static address, as the menu's default next to Windows Boot Manager; its loaders and partition untouched"

step "14. a blank disk whose boot loader goes on another disk's ESP"
stop
boot "$vm/stick.raw" "$vm/sys.raw" "$vm/win2.raw"
install vdb true crossdisk "" vdc1
boot "$vm/sys.raw" "$vm/win2.raw"
booted "$version"
[ "$(vssh cat /proc/sys/kernel/hostname)" = crossdisk ] || die "the host name is $(vssh cat /proc/sys/kernel/hostname)"
vssh 'ls /boot/EFI/Linux/ >/dev/null; findmnt -no SOURCE /boot' | grep -q '^/dev/vdb1$' || die "/boot is not the other disk's ESP: $(vssh 'findmnt -no SOURCE /boot')"
vssh 'ls /boot/EFI/Linux/' | grep -q "^homelab_$version.efi$" || die "the kernel is not on the other disk's ESP"
vssh 'lsblk -no PARTTYPENAME /dev/vda' | grep -q "EFI System" && die "the system disk got an ESP of its own"
vssh 'bootctl list' | grep -q 'id: auto-windows' || die "the boot menu has no Windows entry"
vssh 'bootctl list' | grep -B1 'id: homelab_' | grep -q '(default)' || die "the system is not the boot menu's default"
[ "$(espfiles /dev/vdb1)" = "WINDOWS-BOOTMGR WINDOWS-FALLBACK " ] || die "the other system's loaders changed: $(espfiles /dev/vdb1)"
ok "boots from the other disk's ESP, mounted at /boot; Windows in its menu, its loaders untouched"

printf '\nimage-check passed: %s%s%s\n' "$version" "${next:+ → $next}" "${broken:+, fell back from $broken}"
