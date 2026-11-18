# homelabPkgs — packages built from this repo, as one explicit set: passed to
# NixOS modules as the `homelabPkgs` specialArg and exported as the flake's
# packages/checks. There is no overlay; consumers name what they use.
{
  pkgs,
  # bcdu's source (the flake input), for the core extension's static build.
  bcduSrc ? null,
}:
import ./homelab.nix { inherit pkgs bcduSrc; }
