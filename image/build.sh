#!/usr/bin/env bash
# build.sh [VERSION]: the image (mkosi, Arch) and the core extension (Nix)
# into image/out, named for systemd-sysupdate:
#   homelab_VERSION.raw                     the whole disk (installer, VM; not
#                                           for an update, see below)
#   homelab_VERSION.efi                     the UKI
#   homelab_VERSION.root.UUID.raw           one root slot, and its verity hash
#   homelab_VERSION.root-verity.UUID.raw
#   core/homelab-core_VERSION.raw           the core system extension
# VERSION defaults to the newest release tag and the commit time in UTC
# (X.Y.Z.YYYYMMDDHHMM after tag vX.Y.Z; 0.0.YYYYMMDDHHMM before the first
# release). The same commit builds the same files, a later commit sorts after
# it, and a build of main sorts after the release it follows (0.1.0 <
# 0.1.0.202611190130) and before the next one (0.1.1). A release passes its
# own VERSION.
#
# mkosi runs in an Arch build root of the image's own snapshot, under
# systemd-nspawn: mkosi from nixpkgs cannot bootstrap Arch on NixOS (its
# pacman runs no package scriptlets, and its sandbox cannot reach the host's
# certificates). The build root is made once, shared by every checkout, in
# $HOMELAB_IMAGE_CACHE (default /var/cache/homelab-image). Needs sudo, or
# root: the image host's daemon builds its own updates with this script.
# HOMELAB_IMAGE_OUT moves the output (default image/out).
#
# mkosi keeps the snapshot's packages in the cache, so a second build
# downloads none, and a build whose download failed tries again (three
# tries) without fetching the same packages twice. HOMELAB_IMAGE_UPDATE=1 builds an update for the host it
# runs on (the daemon sets it): mkosi starts from the OS tree of the build
# before (Incremental=) when the packages are the same, and the whole-disk
# .raw, which only an installer needs, is removed.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
if [ -n "${1:-}" ]; then
  version=$1
else
  # awk reads every line, so git never sees a closed pipe.
  base=$(git -C "$root" tag -l --sort=-v:refname | awk '!f && /^v[0-9]+\.[0-9]+\.[0-9]+$/ { sub(/^v/, ""); print; f = 1 }')
  version=${base:-0.0}.$(TZ=UTC git -C "$root" log -1 --format=%cd --date=format-local:%Y%m%d%H%M)
fi
cache=${HOMELAB_IMAGE_CACHE:-/var/cache/homelab-image}
out=${HOMELAB_IMAGE_OUT:-$here/out}
# Root (the daemon on the image host) needs no sudo, and may not have it.
sudo=sudo
[ "$(id -u)" = 0 ] && sudo=

# The snapshot every package comes from, the build root's included.
snapshot=$(sed -n 's/^Snapshot=//p' "$here/base/mkosi.conf")
# Arch's bootstrap tarball the build root starts from, and its sha256 from
# the archive's sha256sums.txt.
bootstrap_date=2026.11.01
bootstrap_sha256=655bc99849b80859173fa39338a6c910185e952173f6a1a61526cfa34ab8339c

buildroot=$cache/buildroot-${snapshot//\//-}
# mkosi's package cache and, for updates, its incremental cache, both for
# this snapshot only.
mkcache=$cache/mkosi-${snapshot//\//-}
update=${HOMELAB_IMAGE_UPDATE:-}
nspawn() {
  $sudo systemd-nspawn -q -D "$buildroot" --resolv-conf=copy-host --pipe \
    --setenv=PATH=/usr/local/sbin:/usr/local/bin:/usr/bin "$@" 2> >(grep -v 'future version of systemd-nspawn' >&2)
}

if ! $sudo test -e "$buildroot/.ready"; then
  echo "build root for snapshot $snapshot"
  $sudo mkdir -p "$cache"
  tarball=$cache/archlinux-bootstrap-$bootstrap_date-x86_64.tar.zst
  if ! $sudo test -e "$tarball"; then
    $sudo curl -sSfL -o "$tarball" "https://archive.archlinux.org/iso/$bootstrap_date/archlinux-bootstrap-$bootstrap_date-x86_64.tar.zst"
  fi
  echo "$bootstrap_sha256  $tarball" | sha256sum -c --quiet
  $sudo rm -rf "$buildroot.tmp"
  $sudo mkdir -p "$buildroot.tmp"
  $sudo tar --numeric-owner --zstd -xpf "$tarball" -C "$buildroot.tmp" --strip-components=1 2> >(grep -v 'unknown extended header' >&2)
  echo "Server = https://archive.archlinux.org/repos/$snapshot/\$repo/os/\$arch" | $sudo tee "$buildroot.tmp/etc/pacman.d/mirrorlist" >/dev/null
  $sudo mv "$buildroot.tmp" "$buildroot"
  nspawn /usr/bin/bash -c 'pacman-key --init >/dev/null 2>&1 && pacman-key --populate archlinux >/dev/null 2>&1 &&
    pacman -Syu --noconfirm --needed mkosi systemd-ukify dosfstools mtools erofs-utils squashfs-tools cpio zstd xz openssl git >/dev/null'
  $sudo touch "$buildroot/.ready"
fi

echo "image $version (snapshot $snapshot, $(nspawn /usr/bin/mkosi --version))"
mkdir -p "$out/core"
# An earlier build of the same version leaves root files with other UUIDs,
# which SHA256SUMS would list next to this build's.
$sudo rm -f "$out"/homelab_"$version".* "$out/core/homelab-core_$version.raw"
# Another snapshot's caches are of no further use.
$sudo find "$cache" -mindepth 1 -maxdepth 1 -name 'mkosi-*' ! -path "$mkcache" -exec rm -rf {} +
$sudo mkdir -p "$mkcache/images" "$mkcache/packages"

# The core extension first: the disk image is also the installer stick, and
# its /var starts with the core (where sysupdate keeps it) and the marker
# that starts the daemon in installer mode. HOMELAB_BUILD_JOBS limits Nix's
# parallel builds: the host building its own update (the daemon sets 1) may
# have little memory to spare.
ext=$(nix --extra-experimental-features 'nix-command flakes' build --max-jobs "${HOMELAB_BUILD_JOBS:-auto}" \
  --no-link --print-out-paths "$root#core-extension")
install -m644 "$ext/homelab-core.raw" "$out/core/homelab-core_$version.raw"
tree=$out/var-tree
$sudo rm -rf "$tree"
mkdir -p "$tree/var/lib/extensions.d" "$tree/var/lib/extensions" "$tree/var/lib/homelab"
cp "$out/core/homelab-core_$version.raw" "$tree/var/lib/extensions.d/"
ln -s "/var/lib/extensions.d/homelab-core_$version.raw" "$tree/var/lib/extensions/homelab-core.raw"
echo "Remove this file and the system leaves installer mode." > "$tree/var/lib/homelab/installer"
$sudo chown -R 0:0 "$tree"

# mkosi's workspace is in the /out bind, so it renames its outputs into
# place: from another mount it copies them, gigabytes each time.
work=$out/.mkosi-work
# pacman gives up on a download that stalls for 10 s, and the Arch archive
# stalls now and then. The packages it did fetch stay in the package cache,
# so another try fetches only the rest. Any other failure ends the build.
log=$(mktemp)
for try in 1 2 3; do
  $sudo rm -rf "$work"
  mkdir -p "$work"
  if nspawn --bind="$here:/work" --bind="$out:/out" --bind="$mkcache:/cache" --chdir=/work/base \
    /usr/bin/mkosi --image-version "$version" --output-directory /out --workspace-directory /out/.mkosi-work \
    --cache-directory /cache/images --package-cache-dir /cache/packages \
    ${update:+--incremental=yes} --extra-tree=/out/var-tree:/ --force build 2>&1 | tee "$log"; then
    break
  fi
  if [ "$try" = 3 ] || ! grep -q 'failed retrieving file' "$log"; then
    rm -f "$log"
    exit 1
  fi
  wait=$((try * 15))
  echo "a package download failed (try $try of 3); waiting ${wait}s before trying again"
  sleep "$wait"
done
rm -f "$log"
$sudo rm -rf "$tree" "$work"
# An update needs no installer image; repart writes it with the split files.
[ -z "$update" ] || $sudo rm -f "$out/homelab_$version.raw"
$sudo chown -R "$(id -u):$(id -g)" "$out"

# sysupdate reads a manifest in each folder.
(cd "$out" && sha256sum homelab_"$version".efi homelab_"$version".root*.raw > SHA256SUMS)
(cd "$out/core" && sha256sum homelab-core_"$version".raw > SHA256SUMS)
ls -l "$out" "$out/core"
