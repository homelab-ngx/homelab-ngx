# The files

Every stack repo has `homelab-stack.json` (the manifest: what the stack asks of the host) and `flake.nix`, which exports the stack's NixOS module as `nixosModules.default`. A repo may hold more files the module imports or reads; keep to how it is laid out. The flake needs no inputs; for a new stack, write the module inline in flake.nix as `outputs = _: { nixosModules.default = { config, lib, pkgs, ... }: { … }; };`. The host builds the module on its own base module, with the nixpkgs the manifest names, so the whole stack uses one nixpkgs.

## homelab-stack.json (manifest version 8)

Unknown fields are refused. Fields:

- `"version": 8` and `"name"`: the stack's suggested name, 1-12 characters of a-z, 0-9 and '-', starting with a letter.
- `"nixpkgs"` (required): `"nixos-unstable"`, `"nixos-YY.MM"` or a 40-character NixOS/nixpkgs commit.
- `"routes"`: `[{ "host": "<label>", "port": <port in the stack>, "max_body": "50GB" (optional) }]`. A label such as `"photos"` is served privately as `photos.<the host's domain>` over HTTPS through the host's Caddy. Each route is a grant the person approves.
- `"variables"`: values the host gives the stack. Each is `{ "name", "kind", "description", "required" }` plus fields by kind. Names match `^[A-Z][A-Z0-9_]{0,63}$`. Write `description` as one short sentence that says what the value is, with an example where it helps ("The tunnel address, such as 10.2.0.2/32."); the dashboard shows it as the help for that variable.
  - `"env"`: a plain value. No default in the manifest: the value the module sets is the default. Optional `"type"` (`string`, `number`, `boolean`) and `"enum"` (allowed values).
  - `"secret"`: write-only, no default.
  - `"path"`: a host folder the person picks, bound into the stack at `"mount"` (required; an absolute path inside the stack, used once). Optional `"default"` (a host folder), `"read_only": true`, and `"owner"` / `"group"`: a user and a group the module declares, which then take the host folder's uid and gid. The person makes the folder from the stack's page, which gives it the host's ids for those names; they never pick a number. A path is a grant.
  - `"address"`: IPv4 addresses or CIDRs, comma-separated, used by egress and ports as `"$NAME"`. No default.
  The names `HOMELAB_API_TOKEN`, `HOMELAB_API_URL`, `HOMELAB_DOMAIN` and `HOMELAB_DASHBOARD_URL` are the host's; do not declare them.
- `"egress"`: `[{ "to": "host" | "<CIDR>" | "$NAME", "port": <n>, "proto": "tcp" | "udp" }]`. The stack reaches the internet and the host's Caddy on 443 without one; the LAN, other host ports, Tailscale and IPv6 are blocked unless opened here. The host's mail relay is `"to": "host", "port": 25`.
- `"ports"`: `[{ "port": <host port>, "to": <stack port>, "proto": "tcp" | "udp", "from": ["lan" | "<CIDR>" | "$NAME"] }]`, for protocols Caddy cannot carry (RTSP, a game server, a media server's direct connections). Never 22, 53, 80 or 443.
- `"gpu": true`: the host's GPU devices and drivers.
- `"containers": true`: podman may run in the stack.
- `"api": ["read"]` and/or `"control"`: the stack may call the homelab daemon's API.
- `"health"` (required in practice): `{ "units": ["<unit>.service", …], "http": { "port": <n>, "path": "/…" } }`. Every listed unit must be active and the HTTP probe must answer 2xx on the stack's address, or an update is rolled back. List every service that must be up.
- `"backup"`: `{ "prepare": "<oneshot unit>" (optional), "folders": [{ "label", "path": "/var/lib/…" } or { "label", "variable": "<PATH VARIABLE>" }, with optional "exclude": [{ "pattern": "/cache/**", "reason": "…" }]] }`. Recommend data, not caches; patterns start with `/` or `**/`; `path` may not be `/`. For a database, dump it on a timer to a folder (`services.postgresqlBackup`) and name that unit as `prepare`.

## The NixOS module (flake.nix)

The host's base module already makes the system a container with networking (`boot.isContainer`, systemd-networkd, resolved) and sets the hostname and time zone. The module holds only the stack's own services. Rules:

- Plain NixOS options only. There are no `homelab.*` options.
- Services that Caddy or the health check reach listen on all addresses (`0.0.0.0`), never `127.0.0.1`. The host opens the routes', the health probe's and the ports' targets in the stack's firewall: set no firewall options and no `hardware.graphics`.
- Keep state under `/var/lib/<service>`; prefer the NixOS module's own state directory.
- A path variable is used at its `mount`; nothing in the module names a host folder. When a service writes there as its own user, give the variable `"owner"` (and `"group"`) and declare that user and group in the module (`users.users.<name> = { isSystemUser = true; group = "<group>"; };`, `users.groups.<group> = { };`) and run the service as them.
- A secret is read from the file `/run/secrets/<NAME>` (no trailing newline). A root service reads it directly; a service with its own user takes it with `systemd.services.<unit>.serviceConfig.LoadCredential = [ "name:/run/secrets/NAME" ];` and reads `$CREDENTIALS_DIRECTORY/name`. Never put a secret value in the files.
- Every env and address value is also in `/run/secrets/env` as `NAME=value`, which the base module adds as the last `EnvironmentFile=` of every service, so a value the person sets overrides the module's `Environment=`.
- Software not in nixpkgs is built inside the module with `pkgs.callPackage` or a derivation written inline.
- Podman containers (with `"containers": true`) run from units via `virtualisation.oci-containers`, with images pinned by digest (`image@sha256:…`); a container reaches the host as the stack's address.
- Pin a database's major version (`services.postgresql.package = pkgs.postgresql_17;`).
- `system.stateVersion` is set by the base module; set it only to keep an older one.
