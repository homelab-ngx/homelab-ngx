#!/usr/bin/env bash
# scripts/update-hashes.sh
# Usage: ./scripts/update-hashes.sh [daemon|dashboard|all]
set -euo pipefail

TARGET=${1:-all}
# Hashes live in nix/pkgs/homelab.nix, not flake.nix
OVERLAY="$(git rev-parse --show-toplevel)/nix/pkgs/homelab.nix"

get_hash() {
  local pkg=$1
  ( nix build ".#${pkg}" --no-link 2>&1 || true ) | grep "got:" | awk '{print $2}' | tail -1
}

patch_hash() {
  local field=$1 hash=$2
  sed -i "s|${field} = \"sha256-[^\"]*\"|${field} = \"${hash}\"|" "$OVERLAY"
}

stage_overlay() {
  git add "$OVERLAY"
}

if [[ "$TARGET" == "daemon" || "$TARGET" == "all" ]]; then
  echo "==> Refreshing daemon vendorHash..."
  patch_hash vendorHash "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
  stage_overlay
  HASH=$(get_hash daemon)
  if [[ -n "$HASH" ]]; then
    patch_hash vendorHash "$HASH"
    stage_overlay
    echo "Successfully updated vendorHash to: $HASH"
  else
    echo "Failed to get new vendorHash. Is the build succeeding without hash failure?" >&2
  fi
fi

if [[ "$TARGET" == "dashboard" || "$TARGET" == "all" ]]; then
  echo "==> Refreshing dashboard npmDepsHash..."
  patch_hash dashboardNpmDepsHash "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
  stage_overlay
  HASH=$(get_hash dashboard)
  if [[ -n "$HASH" ]]; then
    patch_hash dashboardNpmDepsHash "$HASH"
    stage_overlay
    echo "Successfully updated dashboardNpmDepsHash to: $HASH"
  else
    echo "Failed to get new dashboardNpmDepsHash. Is the build succeeding without hash failure?" >&2
  fi
fi

echo "Done. Verifying build..."
if [[ "$TARGET" == "all" ]]; then
  nix build .#daemon .#dashboard --no-link
else
  nix build ".#$TARGET" --no-link
fi
echo "Build verification successful!"
