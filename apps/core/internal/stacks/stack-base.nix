# The base of every stack built from manifest version 7: what running as a
# homelab stack needs, so a stack's repo carries only its own services. The
# daemon builds it together with the stack's nixosModules.default and a
# module that sets the hostname (the installed stack name) and the time zone
# (the host's). Also exported as the homelab flake's nixosModules.stack-base.
{ lib, ... }:
{
  # Every service reads /run/secrets/env last: the env values the operator
  # set on the host override the ones the stack's flake gives. systemd lets an
  # EnvironmentFile= override Environment=; "-" makes a missing file fine.
  options.systemd.services = lib.mkOption {
    type = lib.types.attrsOf (
      lib.types.submodule {
        config.serviceConfig.EnvironmentFile = lib.mkAfter [ "-/run/secrets/env" ];
      }
    );
  };

  config = {
    boot.isContainer = true;
    # nspawn mounts /run, /dev/shm and friends; inside the stack's user
    # namespace they cannot be remounted, and without this an in-place update
    # fails and the host restarts the stack instead.
    system.activationScripts.specialfs = lib.mkForce "";

    # The host hands in the address, gateway and DNS for host0 as a networkd
    # file bound at /run/systemd/network.
    networking.useHostResolvConf = false;
    networking.useDHCP = false;
    networking.useNetworkd = true;
    services.resolved.enable = true;

    # A stack that keeps state across NixOS releases sets its own.
    system.stateVersion = lib.mkDefault "26.11";
  };
}
