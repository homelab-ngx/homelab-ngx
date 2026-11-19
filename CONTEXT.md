# homelab-ngx

A home server OS: a small read-only image (core) that one privileged daemon
supervises, with a dashboard and a CLI as its two clients, and every service
in a stack of its own.

## Language

### Services

**Core service**:
Baseline infrastructure on the host (the daemon, Caddy, the tunnel, the audit timer), owned by systemd and only displayed by the daemon. Every other service runs in a stack.
_Avoid_: system service, infra service, managed service

**Stack** (service stack):
A repo with a flake and a manifest that runs as its own NixOS system in a systemd-nspawn container, installed and pinned by the daemon. Holds one or more services that work together (`immich`; `players` = Plex and Jellyfin; `arr` = the arrs and their download client).
_Avoid_: group (also a unix group), container, template (reserved for a future stack with fill-in placeholders)

**Stack name**:
The name a stack is installed under (`players`), chosen at install and fixed after it: the key of its container, units (`players-plex`), state and everything the daemon keeps for it. The manifest's name only suggests it.
_Avoid_: id, slug

**Stack label**:
The name the dashboard shows for a stack, set by the operator and changeable at any time; empty shows the stack name. Renaming a stack sets its label.
_Avoid_: display name, title (in the API)

**Manifest**:
`homelab-stack.json` in a stack's repo: what the stack asks of the host. Its `name` is the repo's own name, the suggested stack name. From version 7 its `nixpkgs` (a channel or a commit) is the nixpkgs the stack is built with, and the repo's flake exports `nixosModules.default`; up to version 6 the flake output built is `nixosConfigurations.<manifest name>`. Version 8 lets a repo name nothing of its host: route labels, address variables, a path's owner and group.
_Avoid_: config, template

**Base module**:
The daemon's NixOS module every version 7 stack is built on (`stack-base.nix`): container, network, hostname, time zone, a default stateVersion, and the env file that lets host values override the flake's. It keeps stack repos to their own services.
_Avoid_: base image, template, stack base config

**Draft**:
A stack written in the dashboard: a git repo the daemon keeps at `/var/lib/homelab-daemon/drafts/<name>`, holding the manifest and `flake.nix`, started from an embedded template. Save is a commit; Check reads the manifest as an install would; Build builds the saved commit as an install would, without installing it. Installed, it is an ordinary stack whose source is that repo (`git+file://`), and each save shows as an update. The drafts folder is in the Core backup.
_Avoid_: scratch stack, local stack, template

**Assistant**:
The optional language model the stack editor asks for help (Claude, or an OpenAI-compatible endpoint), set up in System Settings. One request per question: the draft's two files, what Check and the last Build printed, and the authoring guide (`internal/drafts/guide.md`). No tools, no loop, no variable values; its answer is notes and whole files the operator applies or discards.
_Avoid_: agent, copilot, AI

**Host module**:
What the daemon writes into a version 8 stack's build from its manifest and the host: the ids its path owners and groups take, the firewall ports its routes, health probe and port grants need, and the host's GPU drivers.
_Avoid_: generated config, overrides

**Route label**:
A version 8 route's `host` without a dot (`photos`), served as `photos.<the host's private domain>`; the stack reads the domain as `HOMELAB_DOMAIN`.
_Avoid_: subdomain, vhost

**Address variable**:
A stack variable of kind `address` (version 8): IPv4 addresses or CIDRs the operator gives. An egress destination or a port grant's client written `$NAME` opens to them, so the repo names no LAN address. Setting it is the approval.
_Avoid_: host variable, IP setting

**Grant**:
One thing a manifest asks for that the operator must approve: a route (hostname), a storage path (a path variable), the GPU, an egress destination, a port forwarded from the host, running containers, or an API scope.
_Avoid_: permission, capability

**API grant**:
A manifest's `api` entry (version 3): the scopes `read` (every GET) and `control` (start, stop, restart of stack units and stacks), each approved like any other grant, that let the stack call the daemon on the stack listener.
_Avoid_: API access level, role

**Stack token**:
The bearer token a stack with an API grant uses on the stack listener; minted on approve, stored only as its SHA-256, handed to the stack in `/var/lib/homelab-api/env` as `HOMELAB_API_TOKEN`.
_Avoid_: API key, secret (a stack variable is something else)

**Stack variable**:
A value a stack gets from the host instead of its repo. The manifest (version 5) declares each one with a kind: env (a plain value), secret (write-only, no default), path (a host directory the operator picks, bound at the variable's mount) or, from version 8, address (see Address variable). Each is required or optional, and env and path may have a default. The operator may add env variables of their own (plain unless marked secret). The value is the stored one, else the default, else unset; an install or update waits until every required one has a value. Stored age-encrypted in the stack's state directory; env and secret values are delivered at `/run/secrets/<NAME>` and, when env-safe, in `/run/secrets/env`, and a path as a bind.
_Avoid_: stack secret key, sops secret (stacks hold no sops), env var (only the env-safe ones reach `env`)

**Stack listener**:
The daemon's second API, on TCP 7781, which a stack reaches at its host address (`10.99.N.1`), serving only read and control operations to a stack whose token, source address and scope check out.
_Avoid_: public API, stack API server

**Stack unit**:
A service unit inside a stack, listed on the host as `<stack>-<unit>` (`players-plex`), with start, stop, restart and logs going into the stack.
_Avoid_: managed service

**Update attempt**:
One install, update or rollback of a stack, kept in `stack_updates` with its result (healthy, rolled back, build failed, failed, interrupted), reason, log and package changes. A stack's History dialog on its dashboard page reads them.
_Avoid_: deployment, release

**Core deploy**:
One update of core, or one rollback to the version before, started through the daemon (dashboard or CLI). On an image host it installs a new core extension or image with systemd-sysupdate and checks the daemon and Caddy; if they do not come back the host returns to the version before. Its result is healthy, rolled back, build failed, failed or interrupted.
_Avoid_: update attempt (that is a stack's), release

**Stack lock**:
`stacks.lock.json` in the stacks state directory: each approved stack's source, commit, manifest and subnet, from which `homelab stacks restore` reinstalls the stacks after the daemon's database is lost.
_Avoid_: flake.lock (a stack's own input pins)

**Unit**:
The systemd unit name that identifies a service (`homelab-daemon`, or `arr-sonarr` for a stack unit); the key everywhere in the API and database.
_Avoid_: service id, container name

### Backups

**Backup**:
A named set of sources copied to one or more destinations on a schedule. Stored in the database and edited in the dashboard; nothing about it is in Nix.
_Avoid_: job, backup unit

**Destination**:
Where a backup is copied, with its credentials: an S3 bucket or an rclone remote (both a mirror of the folders), or a restic repository (snapshots).
_Avoid_: target, remote, bucket (a bucket is one kind)

**Source** (in backups):
One thing a backup includes: a recommended folder, a custom folder on the host, or Core.
_Avoid_: path, include

**Recommended folder**:
A folder a stack's manifest declares as worth backing up. It is off until the operator adds it to a backup.
_Avoid_: backup path, declared folder

**Not backed up**:
A recommended folder that no enabled backup with a destination includes.
_Avoid_: uncovered (the API's word), unprotected

**Core** (in backups):
The built-in source for the daemon's own state: a fresh database snapshot, every stack's stored variables, and the stack lock.
_Avoid_: system backup, config backup

**Prepare unit**:
A unit inside a stack, named in its manifest, that a backup run starts and waits for before copying that stack's folders (its database dump).
_Avoid_: hook, pre-backup script

**Run**:
One execution of a backup, with a result per destination and per folder: success, partial, failure, cancelled or unknown.
_Avoid_: job, execution

**Runner**:
`homelab-backup-runner`, the program that does a run's copying with rclone and restic inside a sandboxed transient unit. The daemon starts it and reads its state file; it never copies anything itself.
_Avoid_: worker, agent

### Metrics

**Core** (in metrics):
The host less every stack: the OS, the daemon, Caddy, the other core services and the kernel's own work (the file system). The overview shows it as its own band, in the core violet.
_Avoid_: system, other, host (the host is the whole machine, core included)

**Pressure**:
The share of time some task waited for the processor, memory or disks (Linux PSI, the "some" line), for the host and per stack. The overview's mood (calm, busy, struggling) comes from it; CPU use alone never makes the host "struggling". The dashboard calls it "Waiting".
_Avoid_: load, load average

### Configuration

**Nix config**:
The read-only description of the host rendered at build time into the daemon's `config.json` (on the image, by the core extension).
_Avoid_: services.yaml, static config

**Snapshot**:
An immutable view of the effective configuration (the nix config plus the pools) that readers take once per request or tick.
_Avoid_: config copy, cache

**Pool**:
A bcachefs storage pool recorded by the daemon, including whether it is auto-mounted at boot; owned by the database, not Nix.
_Avoid_: array, volume

**Browse root**:
A folder the dashboard's file browser may list: a subvolume, or a host folder bound into a stack. Above a browse root the browser shows only the folders that lead to one; nothing else on the host is listed.
_Avoid_: share, mount

### Alerting

**Problem**:
An ongoing bad condition identified by a key; opens once, reminds daily, and resolves when it clears.
_Avoid_: alert, incident

**Problem key**:
The stable identifier of a problem, prefixed by scope: `audit:<check>`, `unit:<unit>`, `backup:<id>`, `backup-stale:<id>`, `churn:<unit>`.
_Avoid_: alert id, dedup key

**Churn**:
A unit restarting repeatedly (five or more starts within an hour), detected by the daemon.
_Avoid_: flapping, crash loop

**Image**:
The host's operating system as one built, read-only disk image (Arch, made with mkosi), installed in two slots and updated whole with systemd-sysupdate; a broken update falls back to the slot before.
_Avoid_: firmware, appliance

**Core extension**:
The daemon, CLI, dashboard and Caddy as one system extension (`homelab-core`) merged over the image's /usr; updated without a reboot.
_Avoid_: core package, sysext (in prose)

**Release**:
A version of the image and the core extension (`vX.Y.Z`) built, checked and signed by the release workflow and published on the release server (`dl.homelab-ngx.com`). An image host looks for a newer one daily, downloads and verifies it, and installs it only when the operator asks. A build of main is versioned above the release it follows (`0.1.0.202611190130`).
_Avoid_: update (that is the install), core deploy

**Host settings**:
An image host's own network setup, edited on System Settings › Network: domain and certificate, LAN, firewall openings, the VPN and its devices, the tunnel's routes, Tailscale, host name and time zone. Kept in SQLite and applied by the daemon at once; read-only on a NixOS host.
_Avoid_: host config (that is config.json), network settings

**Notification**:
One message sent to every enabled notification channel and logged with its delivery attempts, one per channel.
_Avoid_: alert (a notification is a message, a problem is a condition)

**Notification channel**:
Where notifications go, added on the Notifications page: an ntfy topic or a webhook that takes the generic JSON payload. Its token is sealed to the host's age key.
_Avoid_: notifier (the code that sends), provider

### Health checking

**Doctor**:
The CLI health check that looks only at what the daemon cannot see about itself.
_Avoid_: health check, audit (the audit is the daily doctor run)

**Check result**:
The outcome of one doctor check: `ok`, `fail` (confirmed problem) or `unknown` (could not tell).
_Avoid_: pass/fail

**Streak**:
The count of consecutive `fail` results for a check; two open a problem.
_Avoid_: strike, retry count

### Architecture

**Feature package**:
A Go package that owns one domain end to end, its logic and its HTTP adapter, built by an explicit constructor.
_Avoid_: module, layer, service (overloaded)

**Composition root**:
The one place (`cmd/daemon/main.go`) where every feature package is constructed and the boot order is written out.
_Avoid_: bootstrap, app

**Contract**:
The OpenAPI document the daemon emits from its handlers (`api/openapi.json`), from which the dashboard's types are generated.
_Avoid_: schema, API definition, spec (there is no hand-written spec)

**Scope**:
Who may call an API operation, set when it is registered: `operator` (the unix sockets only), `read` or `control` (also on the stack listener).
_Avoid_: permission, role

**Sign-in**:
The dashboard's one password and the browser session it starts. It guards the web socket Caddy uses; the CLI's socket has none.
_Avoid_: login, user, account (there is one password, no users)

**Outside view**:
Observation of the host from a vantage independent of the daemon.
_Avoid_: external check
