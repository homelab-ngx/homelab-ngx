# Flake outputs. Inputs live in ../flake.nix.
{ nixpkgs, ... }@inputs:
let
  system = "x86_64-linux";
  pkgsFor =
    system:
    import nixpkgs {
      inherit system;
      config.allowUnfree = true;
    };
  pkgs = pkgsFor system;
  # The dev shells and the formatter also exist on a Mac, where code, tests
  # and generation run. The packages, checks and the image are Linux only.
  devSystems = [
    system
    "aarch64-darwin"
  ];
  # Packages built from this repo (no overlay — consumers name them).
  homelabPkgs = import ./pkgs {
    inherit pkgs;
    bcduSrc = inputs.bcdu;
  };
in
{
  nixosModules = {
    # What the daemon builds every version 7 stack on, for a stack's own
    # tests outside the daemon (`homelab stacks build` uses the same file).
    stack-base = ../apps/core/internal/stacks/stack-base.nix;
  };

  packages.${system} = {
    default = homelabPkgs.homelab-daemon-binary;
    daemon = homelabPkgs.homelab-daemon-binary;
    cli = homelabPkgs.homelab-daemon-cli;
    dashboard = homelabPkgs.homelab-dashboard;
    core-extension = homelabPkgs.homelab-core-extension;
    inherit (homelabPkgs)
      homelab-daemon
      homelab-backup-runner
      ;
  };

  devShells = nixpkgs.lib.genAttrs devSystems (
    s:
    let
      shells = import ./devshells.nix { pkgs = pkgsFor s; };
    in
    if s == system then shells else removeAttrs shells [ "image" ]
  );

  checks.${system} = {
    daemon = homelabPkgs.homelab-daemon-binary;
    dashboard = homelabPkgs.homelab-dashboard;
    inherit (homelabPkgs.homelab-checks)
      daemon-vet
      daemon-test
      backup-runner-test
      codegen-drift
      dashboard-test
      dashboard-schema-drift
      ;
  };

  formatter = nixpkgs.lib.genAttrs devSystems (s: (pkgsFor s).nixfmt-tree);
}
