{
  description = "NAME, a homelab stack";

  # The homelab daemon builds nixosModules.default into a NixOS system of its
  # own, on its base module (a container with networking) and with the nixpkgs
  # that homelab-stack.json names. The flake needs no inputs.
  outputs = _: {
    nixosModules.default =
      { ... }:
      {
        # A plain NixOS module that holds only this stack's services. This one
        # serves the folder SITE_DIR as web pages; replace it with your own.
        services.nginx = {
          enable = true;
          virtualHosts.default = {
            default = true;
            # Listen on the stack's address, not 127.0.0.1: Caddy on the host
            # and the health check reach it there. The host opens the route's
            # and the health check's ports in the stack's firewall.
            listen = [
              {
                addr = "0.0.0.0";
                port = 8080;
              }
            ];
            # SITE_DIR is bound here: its "mount" in homelab-stack.json.
            root = "/srv/site";
            extraConfig = "autoindex on;";
          };
        };
      };
  };
}
