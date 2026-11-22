#!/usr/bin/env bash
# variant.sh FROM VERSION: a second version from a built one, in seconds
# instead of a build. The image check needs a version only to install it and
# break it (step 11), and a build for that is the same system anyway.
#
# The update files of FROM in image/out are copied as VERSION's:
#   - the root with its unused first bytes changed (erofs starts at byte
#     1024), so it has a verity hash and partition UUIDs of its own; two
#     slots with the same UUIDs on one disk could not be told apart;
#   - a new verity hash file for it;
#   - the UKI with that root hash in its command line and VERSION in its
#     os-release, which the boot menu sorts by;
#   - the core extension, which is the same file in every version.
# The root's own /etc/os-release still says FROM, so a variant is not for
# booting and not for release. No installer medium is made.
# Run inside the image shell (`make image-variant FROM= VERSION=`).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${HOMELAB_IMAGE_OUT:-$here/out}
from=${1:?variant.sh FROM VERSION}
version=${2:?variant.sh FROM VERSION}
[ "$from" != "$version" ] || { echo "variant: $version is the version it would copy" >&2; exit 1; }

uki=$out/homelab_$from.efi
root=$(ls "$out"/homelab_"$from".root.*.raw 2>/dev/null || true)
core=$out/core/homelab-core_$from.raw
[ -e "$uki" ] && [ -e "$core" ] && [ "$(wc -l <<<"$root")" = 1 ] && [ -n "$root" ] ||
  { echo "variant: image $from is incomplete in $out: run make image VERSION=$from" >&2; exit 1; }
oldhash=$(sed -E 's|.*\.root\.([0-9a-f]{32})\.raw$|\1|' <<<"$root")
oldverity=$(ls "$out"/homelab_"$from".root-verity.*.raw)
oldhash+=$(sed -E 's|.*\.root-verity\.([0-9a-f]{32})\.raw$|\1|' <<<"$oldverity")

rm -f "$out"/homelab_"$version".* "$out/core/homelab-core_$version.raw"
tmp=$(mktemp -d "$out/variant.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

cp --sparse=always "$root" "$tmp/root"
[ "$(head -c 1024 "$tmp/root" | tr -d '\0' | wc -c)" = 0 ] || { echo "variant: the root's first 1024 bytes are in use" >&2; exit 1; }
printf 'variant %s' "$version" | dd of="$tmp/root" conv=notrunc status=none
hash=$(veritysetup format "$tmp/root" "$tmp/verity" | awk '/^Root hash:/ { print $3 }')
[[ $hash =~ ^[0-9a-f]{64}$ ]] || { echo "variant: no root hash from veritysetup" >&2; exit 1; }
# sysupdate writes the file into a partition made for FROM's.
[ "$(stat -c %s "$tmp/verity")" -le "$(stat -c %s "$oldverity")" ] || { echo "variant: the verity file is larger than $from's" >&2; exit 1; }

# The two sections are rewritten in place: each has room to the next 512
# bytes, and nothing after them moves.
python3 - "$uki" "$tmp/uki" "$oldhash" "$hash" "$from" "$version" <<'EOF'
import struct, sys
src, dst, oldhash, newhash, old, new = sys.argv[1:]
pe = bytearray(open(src, "rb").read())
coff = struct.unpack_from("<I", pe, 0x3C)[0] + 4
count, opt = struct.unpack_from("<H", pe, coff + 2)[0], struct.unpack_from("<H", pe, coff + 16)[0]
want = {
    b".cmdline": (b"roothash=" + oldhash.encode(), b"roothash=" + newhash.encode()),
    b".osrel": (b'IMAGE_VERSION="%s"' % old.encode(), b'IMAGE_VERSION="%s"' % new.encode()),
}
for i in range(count):
    at = coff + 20 + opt + 40 * i
    name = bytes(pe[at : at + 8]).rstrip(b"\0")
    if name not in want:
        continue
    a, b = want.pop(name)
    vsize, _, rawsize, raw = struct.unpack_from("<IIII", pe, at + 8)
    data = bytes(pe[raw : raw + vsize])
    if data.count(a) != 1:
        sys.exit(f"variant: {name.decode()} does not hold {a.decode()} once")
    data = data.replace(a, b)
    if len(data) > rawsize:
        sys.exit(f"variant: {name.decode()} has no room for {b.decode()}")
    pe[raw : raw + rawsize] = data.ljust(rawsize, b"\0")
    struct.pack_into("<I", pe, at + 8, len(data))
if want:
    sys.exit("variant: the UKI has no " + ", ".join(n.decode() for n in want))
open(dst, "wb").write(pe)
EOF

mv "$tmp/uki" "$out/homelab_$version.efi"
mv "$tmp/root" "$out/homelab_$version.root.${hash:0:32}.raw"
mv "$tmp/verity" "$out/homelab_$version.root-verity.${hash:32}.raw"
cp "$core" "$out/core/homelab-core_$version.raw"
chmod 644 "$out"/homelab_"$version".* "$out/core/homelab-core_$version.raw"
echo "image $version: a variant of $from, root hash $hash"
ls -l "$out"/homelab_"$version".* "$out/core/homelab-core_$version.raw"
