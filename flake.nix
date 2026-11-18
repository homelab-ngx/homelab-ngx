{
  description = "homelab-ngx: a small home server OS that runs services in stacks";

  inputs = {
    # Builds the daemon, dashboard, core extension and dev shells; moved by hand.
    nixpkgs.url = "github:NixOS/nixpkgs/7fd4004d0511991fff3a0524856a9be09e28c5f0";
    # bcdu measures the storage page's folder sizes.
    bcdu.url = "github:ogglord/bcdu";
    bcdu.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = inputs: import ./nix inputs;
}
