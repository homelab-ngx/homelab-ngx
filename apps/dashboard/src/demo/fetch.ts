/**
 * The demo's API: what the daemon would answer, from the host in host.ts.
 * `gets` has one entry for every GET the daemon's document describes, typed
 * from it, so the type check fails when the API changes and this did not.
 * A change the demo does not act out answers the problem `not_in_demo`.
 */
import type { paths } from "#lib/api/schema.gen.ts";
import type { Schema } from "#lib/api/client.ts";
import { addPoolDisk, approve, createPool, deletePool, DemoProblem, endRemoval, maskService, removePoolDisk, downloadImage, installImage, iso, newHost, power, powerMeter, powerProfile, powerProfiles, powerStatus, freezeThaw, rollback, runBackup, stackOf, startStop, tick, update, type Host } from "./host.ts";
import { history } from "./metrics.ts";

/** A stack's files as the demo shows them: its manifest and a short flake. */
function filesOf(s: Schema<"Stack">) {
  const m = s.approved?.manifest ?? s.fetched.manifest;
  const manifest = JSON.stringify(m, null, 2) + "\n";
  const flake = [
    "{",
    `  description = "${s.label || s.name}, a homelab-ngx stack";`,
    '  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";',
    "  outputs = { nixpkgs, ... }: {",
    "    nixosModules.default = ./module.nix;",
    "  };",
    "}",
    "",
  ].join("\n");
  const file = (path: string, content: string) => ({ path, size: content.length, binary: false, content });
  return {
    source: s.source,
    rev: s.approved?.rev ?? s.fetched.rev,
    files: [file("flake.nix", flake), file("homelab-stack.json", manifest), { path: "icon.png", size: 2048, binary: true, content: "" }],
  };
}

type JsonOf<P extends keyof paths> = paths[P] extends { get: { responses: { 200: { content: { "application/json": infer R } } } } } ? R : never;
type GetPath = { [P in keyof paths]: [JsonOf<P>] extends [never] ? never : P }[keyof paths];

interface Ctx {
  path: Record<string, string>;
  query: URLSearchParams;
  body: Record<string, unknown>;
}

const GB = 1e9;
const MIN = 60_000;
const DAY = 24 * 60 * MIN;

const missing = (what: string) => new DemoProblem(404, "not_found", `There is no such ${what}.`);

function runDetail(h: Host, id: number): Schema<"BackupRunDetail"> {
  const live = h.runs.find((r) => r.id === id);
  if (live) return live;
  for (const b of h.backups) {
    const r = b.recent_runs.find((x) => x.id === id);
    if (!r) continue;
    return {
      ...r,
      progress: null,
      items: b.sources.map((s) => ({
        kind: "folder" as const,
        label: s.label,
        path: s.host_path,
        destination: b.destinations[0]?.name ?? "",
        bytes: Math.round(r.bytes / b.sources.length),
        files: Math.round(r.files / b.sources.length),
        error: r.error,
        note: "",
        result: r.result === "failure" ? ("failure" as const) : ("success" as const),
        started_at: r.started_at,
        finished_at: r.finished_at,
      })),
    };
  }
  throw missing("run");
}

function coverage(h: Host): Schema<"Coverage"> {
  const stacks = h.stacks
    .filter((s) => s.approved?.manifest.backup?.folders?.length)
    .map((s) => ({
      stack: s.name,
      prepare: s.approved!.manifest.backup!.prepare ?? "",
      folders: s.approved!.manifest.backup!.folders!.map((f) => ({
        key: `${s.name}/${f.label}`,
        label: f.label,
        host_path: f.path ?? s.variables.find((v) => v.name === f.variable)?.value ?? "",
        covered_by: h.backups.filter((b) => b.sources.some((x) => x.stack === s.name && x.label === f.label)).map((b) => b.name),
        excludes: [],
        problem: "",
      })),
    }));
  return { stacks, uncovered: stacks.flatMap((s) => s.folders).filter((f) => f.covered_by.length === 0).length };
}

const TREE: Record<string, [name: string, type: "dir" | "file", size: number][]> = {
  "/": [
    ["documents", "dir", 41 * GB],
    ["media", "dir", 3400 * GB],
    ["photos", "dir", 1100 * GB],
    ["scratch", "dir", 220 * GB],
  ],
  "/media": [
    ["films", "dir", 1900 * GB],
    ["music", "dir", 210 * GB],
    ["series", "dir", 1290 * GB],
  ],
  "/photos": [
    ["library", "dir", 980 * GB],
    ["thumbs", "dir", 90 * GB],
    ["upload", "dir", 30 * GB],
  ],
  "/documents": [
    ["archive", "dir", 38 * GB],
    ["consume", "dir", 0],
    ["export-2026.zip", "file", 3 * GB],
    ["paperless.conf", "file", 61],
  ],
  "/scratch": [["restore-test", "dir", 220 * GB]],
};

/** The demo pool's mount; the tree's keys are relative to it. */
const POOL = "/var/mnt/tank";

/** A folder's rows: in the pool from the tree, above it the one folder that leads there, as the daemon does. */
function rowsAt(path: string): (typeof TREE)[string] {
  const p = path.replace(/\/+$/, "") || "/";
  if (p === POOL || p.startsWith(`${POOL}/`)) return TREE[p.slice(POOL.length) || "/"] ?? [];
  const above = p === "/" ? "/" : `${p}/`;
  if (POOL.startsWith(above)) return [[POOL.slice(above.length).split("/")[0]!, "dir", 0]];
  return TREE[p] ?? [];
}

function listing(h: Host, path: string): Schema<"Listing"> {
  const rows = rowsAt(path);
  return {
    path: path || "/",
    root: "/var/mnt/tank",
    total: rows.length,
    truncated: false,
    entries: rows.map(([name, type, size], i) => ({ name, type, size: type === "file" ? size : 0, disk_bytes: size, owner: "root", group: "users", mode: type === "dir" ? "drwxr-xr-x" : "-rw-r--r--", modified_at: iso(h.now() - (i + 1) * 3 * DAY) })),
  };
}

function logs(h: Host, unit: string, lines: number): Schema<"LogPage"> {
  const texts = ["Started.", "health check passed", "listening on :8080", "GET /api/health 200 1ms", "scheduled job finished in 412ms", "cache: 184 entries, 0 evicted", "connection from 10.88.0.1 closed"];
  const n = Math.min(lines || 200, 200);
  const now = Math.floor(h.now() / 15_000) * 15_000;
  return {
    more: false,
    entries: Array.from({ length: n }, (_, i) => {
      const t = now - (n - 1 - i) * 15_000;
      return { cursor: `${unit}-${t}`, time: iso(t), priority: 6, message: texts[(t / 15_000 + unit.length) % texts.length]! };
    }),
  };
}

function installer(): Schema<"InstallerState"> {
  const active = typeof location !== "undefined" && new URLSearchParams(location.search).has("installer");
  const ok = { ok: true };
  return {
    active,
    step: "idle",
    claimed: true,
    mine: true,
    can_restart: false,
    shared_esp: false,
    log: [],
    min_bytes: 64 * GB,
    current: { mode: "dhcp", address: "192.0.2.40/24", gateway: "192.0.2.1", dns: ["192.0.2.1"] },
    machine: { boot_entries: true, memory: 32 * GB, secure_boot: false },
    disks: [
      { name: "nvme0n1", path: "/dev/nvme0n1", model: "Samsung SSD 980 500GB", bus: "nvme", size: 500 * GB, removable: false, in_use: false, table: "none", partitions: [], free: [{ start: 1024 ** 2, size: 500 * GB - 1024 ** 2 }], free_total: 500 * GB, erase: ok, beside: ok },
      {
        name: "sda",
        path: "/dev/sda",
        model: "CT1000MX500SSD1",
        bus: "sata",
        size: 1000 * GB,
        removable: false,
        in_use: false,
        table: "gpt",
        partitions: [
          { number: 1, name: "sda1", path: "/dev/sda1", label: "EFI", fs: "vfat", type: "esp", holds: "esp", start: 1024 ** 2, size: 1 * GB },
          { number: 2, name: "sda2", path: "/dev/sda2", label: "root", fs: "ext4", type: "linux", holds: "linux", start: 1 * GB + 1024 ** 2, size: 600 * GB },
        ],
        free: [{ start: 601 * GB + 1024 ** 2, size: 399 * GB }],
        free_total: 399 * GB,
        erase: ok,
        beside: ok,
      },
    ],
  };
}

const gets: { [P in GetPath]: (h: Host, c: Ctx) => JsonOf<P> } = {
  "/auth": () => ({ password_set: true, signed_in: true }),
  "/backup-coverage": (h) => coverage(h),
  "/backup-destinations": (h) => h.destinations,
  "/backup-runs/{run}": (h, c) => runDetail(h, Number(c.path.run)),
  "/backups": (h) => h.backups,
  "/backups/{id}": (h, c) => {
    const b = h.backups.find((x) => x.id === Number(c.path.id));
    if (!b) throw missing("backup");
    return b;
  },
  "/backups/{id}/logs": (h, c) => {
    const b = h.backups.find((x) => x.id === Number(c.path.id));
    if (!b) throw missing("backup");
    const r = b.recent_runs[0];
    return { logs: r ? `${r.started_at} ${b.name}: started (${r.trigger})\n${r.finished_at ?? ""} ${b.name}: ${r.result}${r.error ? `: ${r.error}` : ""}\n` : "" };
  },
  "/backups/{id}/runs": (h, c) => h.backups.find((x) => x.id === Number(c.path.id))?.recent_runs ?? [],
  "/config": (h) => ({ core_services: ["homelab-daemon.service", "caddy.service"], generation: 42, loaded_at: iso(h.now() - 6 * DAY), pools: h.storage.pools.map((p) => ({ name: p.name, uuid: p.uuid, mountpoint: p.mountdir, auto_mount: p.auto_mount, options: "" })) }),
  "/core-services": (h) =>
    ["homelab-daemon", "caddy", "cloudflared", "tailscaled"].map((name) => ({ name, unit_name: `${name}.service`, description: name, type: "Native" as const, masked: false, active_state: "active", sub_state: "running", started_at: iso(h.now() - 6 * DAY) })),
  "/drafts": () => [],
  "/drafts/{name}": () => {
    throw missing("draft");
  },
  "/host-settings": (h) => ({
    managed: "image",
    applied: { at: iso(h.now() - 6 * DAY), errors: [] },
    host: { hostname: "homelab", timezone: "Europe/Stockholm", ssh_keys: ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDemoKeyDemoKeyDemoKeyDemoKeyDemoKeyDemoKeyDemo demo@laptop"] },
    domain: { domain: "home.example", dashboard_label: "homelab", dns_provider: "cloudflare", dns_credentials_set: true, acme_email: "admin@home.example" },
    network: { lan: ["192.0.2.0/24"], ipv4: { mode: "dhcp", address: "192.0.2.10/24", gateway: "192.0.2.1", dns: ["192.0.2.1"] } },
    wired: [{ name: "enp3s0", mac: "02:00:5e:00:53:10", addresses: ["192.0.2.10"], subnets: ["192.0.2.0/24"] }],
    firewall: { openings: [{ port: 22, proto: "tcp", from: ["192.0.2.0/24"], note: "SSH from the LAN" }] },
    wireguard: { enabled: true, address: "10.99.0.1/24", endpoint: "vpn.home.example:51820", listen_port: 51820, public_key: "DemoDemoDemoDemoDemoDemoDemoDemoDemoDemoDem0=", peers: [{ name: "phone", address: "10.99.0.2/32", public_key: "PhonePhonePhonePhonePhonePhonePhonePhonePho0=" }, { name: "laptop", address: "10.99.0.3/32", public_key: "LaptopLaptopLaptopLaptopLaptopLaptopLaptopL0=" }] },
    tunnel: { enabled: true, credentials_set: true, dns_managed: true, tunnel_id: "00000000-demo-4000-8000-000000000000", routes: [{ hostname: "photos.home.example", target: "http://photos:2283" }] },
    tailscale: { enabled: false },
    ddns: { enabled: true, name: "vpn", status: { record: "vpn.home.example", ips: ["203.0.113.7"], at: iso(h.now() - 3 * MIN) } },
    time: { clock: "", ntp: [] },
    time_sync: { synced: true, server: "0.arch.pool.ntp.org" },
    console: { keyboard: "se" },
  }),
  "/host-settings/keyboard-layouts": () => ({
    layouts: [
      { name: "gb", description: "English (UK)" },
      { name: "us", description: "English (US)" },
      { name: "fr", description: "French" },
      { name: "de", description: "German" },
      { name: "se", description: "Swedish" },
    ],
  }),
  "/host-settings/tailscale/status": () => ({ state: "off", ips: [] }),
  "/host-settings/tunnel/login": () => ({ state: "idle" }),
  "/host-settings/wireguard/public-address": () => ({ ip: "203.0.113.7" }),
  "/installer": () => installer(),
  "/log-viewer-config": (h) => ({
    units: [
      { id: "homelab-daemon.service", label: "Daemon", group: "core" as const },
      { id: "caddy.service", label: "Caddy", group: "core" as const },
      ...h.services.map((s) => ({ id: s.unit_name, label: `${s.stack}: ${s.name}`, group: "stack" as const })),
    ],
  }),
  "/logs": (h, c) => logs(h, c.query.get("unit") ?? "", Number(c.query.get("lines"))),
  "/metrics/history": (h, c) => history(h, c.query.get("range") ?? "24h"),
  "/notification-channels": (h) => [
    { id: 1, name: "Phone", kind: "ntfy", enabled: true, has_token: true, server: "https://ntfy.sh", topic: "homelab-demo", created_at: iso(h.now() - 100 * DAY) },
    { id: 2, name: "Home Assistant", kind: "webhook", enabled: true, has_token: false, url: "https://home.home.example/api/webhook/homelab", created_at: iso(h.now() - 40 * DAY) },
  ],
  "/notifications": (h) => ({
    failing_channels: {},
    open: { "disk:sdb:smart": { title: "Disk sdb reports 8 bad sectors", count: 3, opened_at: iso(h.now() - 2 * DAY) }, "backup:4": { title: "Backup Media, weekly failed", count: 1, opened_at: iso(h.now() - 3 * DAY) } },
    history: [
      { at: iso(h.now() - 2 * DAY), channel: "Phone", ok: true, severity: "alert", title: "Disk sdb reports 8 bad sectors" },
      { at: iso(h.now() - 3 * DAY), channel: "Phone", ok: true, severity: "alert", title: "Backup Media, weekly failed" },
      { at: iso(h.now() - 6 * DAY), channel: "Phone", ok: true, severity: "info", title: "Updated to 0.2.5" },
    ],
  }),
  "/notifications/log": (h) => [
    { id: 3, at: iso(h.now() - 2 * DAY), key: "disk:sdb:smart", kind: "opened", severity: "alert", title: "Disk sdb reports 8 bad sectors", body: "ST8000VN004 in pool tank. The pool keeps two copies of everything.", deliveries: [{ at: iso(h.now() - 2 * DAY), channel: "Phone", ok: true, error: "" }] },
    { id: 2, at: iso(h.now() - 3 * DAY), key: "backup:4", kind: "opened", severity: "alert", title: "Backup Media, weekly failed", body: "the destination did not answer: dial tcp: i/o timeout", deliveries: [{ at: iso(h.now() - 3 * DAY), channel: "Phone", ok: true, error: "" }] },
    { id: 1, at: iso(h.now() - 6 * DAY), key: null, kind: "sent", severity: "info", title: "Updated to 0.2.5", body: "The host runs 0.2.5.", deliveries: [{ at: iso(h.now() - 6 * DAY), channel: "Phone", ok: true, error: "" }] },
  ],
  "/security/audits": () => [],
  "/security/audits/{id}": () => {
    throw missing("audit");
  },
  "/services": (h) => h.services,
  "/settings/assistant": () => ({ provider: "none", model: "", audit_model: "", base_url: "", key_set: false }),
  "/settings/assistant/limits": () => ({ limits: [] }),
  // What the daemon's reporters say about the demo host above.
  "/settings/status": (h, c) => {
    const s = gets["/host-settings"](h, c) as Schema<"HostSettings">;
    const counted = (state: Schema<"TileStatus">["state"], count: number) => (count ? { state, count } : { state });
    const tiles: Record<string, Schema<"TileStatus">> = {
      tailscale: { state: s.tailscale.enabled ? "ok" : "off" },
      wireguard: s.wireguard.enabled ? counted("ok", s.wireguard.peers.length) : { state: "off" },
      tunnel: s.tunnel.enabled && s.tunnel.credentials_set ? counted("ok", s.tunnel.routes.length) : { state: "off" },
      firewall: { count: s.firewall.openings.length },
      ssh: { count: s.host.ssh_keys?.length ?? 0 },
      notifications: { state: "ok", count: 2 },
      assistant: { state: "off" },
      security: { state: "off" },
    };
    if (h.image.pending_reboot || h.image.available.length || h.image.core.available.length) tiles.updates = { state: "info" };
    if (h.power.scheduled) tiles.system = { state: "info" };
    return { tiles };
  },
  "/stack-updates": (h, c) => h.updates.filter((u) => !c.query.get("stack") || u.stack === c.query.get("stack")).slice(0, Number(c.query.get("limit")) || 50),
  "/stack-updates/{id}": (h, c) => {
    const u = h.updates.find((x) => x.id === Number(c.path.id));
    if (!u) throw missing("update");
    return u;
  },
  "/stacks": (h) => h.stacks,
  "/stacks-lock": (h) => ({
    version: 1,
    stacks: h.stacks.map((s, i) => ({ name: s.name, label: s.label, manifest: s.approved!.manifest, nixpkgs: s.nixpkgs, nixpkgs_rev: s.nixpkgs_rev, rev: s.approved!.rev, source: s.source, url: s.approved!.url, subnet: `10.88.${i + 1}.0/24`, want_running: s.want_running })),
  }),
  "/stacks-manifest-schema": () => ({}),
  "/stacks-nixpkgs": () => ({ channels: ["nixos-26.05", "nixos-unstable"] }),
  "/stacks-store": (h) => ({ own_partition: true, collecting: false, total_bytes: 120 * GB, used_bytes: 47 * GB, free_bytes: 73 * GB, freed_bytes: 6 * GB, collected_at: iso(h.now() - DAY), measured_at: iso(h.now() - 10 * MIN) }),
  "/stacks-ids": () => ({ min: 2000, max: 2099, ids: [{ name: "immich", id: 2000 }, { name: "media", id: 2001 }] }),
  "/stacks/{name}": (h, c) => stackOf(h, c.path.name!),
  "/stacks/{name}/files": (h, c) => filesOf(stackOf(h, c.path.name!)),
  "/storage": (h) => h.storage,
  "/storage/browse": (h, c) => listing(h, c.query.get("path") ?? "/"),
  "/storage/file": (h, c) => {
    const path = c.query.get("path") ?? "";
    const content = "PAPERLESS_OCR_LANGUAGE=eng\nPAPERLESS_TIME_ZONE=Europe/Berlin\n";
    const text = path.endsWith(".conf") || path.endsWith(".txt");
    return { path, root: POOL, size: text ? content.length : 3 * GB, modified_at: iso(h.now() - 3 * DAY), version: "demo", binary: !text, content: text ? content : undefined };
  },
  "/storage/browse/usage": (h, c) => ({
    available: true,
    measured_at: iso(h.now() - 60 * MIN),
    folders: Object.fromEntries(listing(h, c.query.get("path") ?? "/").entries.map((e) => [e.name, { total: e.disk_bytes, exclusive: Math.round(e.disk_bytes * 0.97), shared: Math.round(e.disk_bytes * 0.03) }])),
  }),
  "/system-updates/deploys": (h) => h.deploys,
  "/system-updates/image": (h) => h.image,
  "/system/power": (h) => powerStatus(h),
  "/system/power-profile": (h) => powerProfile(h),
  "/system/power-meter": (h) => powerMeter(h),
  "/metrics/power-profiles": (h, c) => powerProfiles(h, c.query.get("range") ?? "7d", c.query.get("idle_only") === "true"),
  "/version": (h) => ({ version: h.image.core.active, commit: "demo" }),
};

/** The changes the demo acts out. Everything else is `not_in_demo`. */
const posts: Record<string, (h: Host, c: Ctx) => { status: number; body?: unknown }> = {
  "/stacks/{name}/approve": (h, c) => ({ status: 200, body: approve(h, c.path.name!, String(c.body.rev ?? "")) }),
  "/stacks/{name}/update": (h, c) => ({ status: 202, body: update(h, c.path.name!) }),
  "/stacks/{name}/rollback": (h, c) => ({ status: 202, body: rollback(h, c.path.name!, Number(c.body.generation)) }),
  "/stacks/{name}/start": (h, c) => ({ status: 202, body: startStop(h, c.path.name!, true) }),
  "/stacks/{name}/stop": (h, c) => ({ status: 202, body: startStop(h, c.path.name!, false) }),
  "/stacks/{name}/freeze": (h, c) => ({ status: 200, body: freezeThaw(h, c.path.name!, true) }),
  "/stacks/{name}/thaw": (h, c) => ({ status: 200, body: freezeThaw(h, c.path.name!, false) }),
  "/backups/{id}/run": (h, c) => ({ status: 202, body: runBackup(h, Number(c.path.id)) }),
  "/system-updates/image/download": (h) => ({ status: 202, body: downloadImage(h) }),
  "/system-updates/image/release": (h, c) => ({ status: 202, body: installImage(h, String(c.body.reboot ?? "")) }),
  "/system/power": (h, c) => ({ status: 202, body: power(h, c.body.action === "poweroff" ? "poweroff" : "reboot", c.body.force === true, c.body.thaw === true) }),
  "/host-settings/wireguard/peers": (h, c) => {
    const settings = gets["/host-settings"](h, c) as Schema<"HostSettings">;
    const peer = { name: String(c.body.name ?? "device"), address: `10.99.0.${settings.wireguard.peers.length + 2}/32`, public_key: "DemoDeviceDemoDeviceDemoDeviceDemoDeviceDem0=" };
    settings.wireguard.peers.push(peer);
    const config = `[Interface]\nPrivateKey = DemoPrivateKeyDemoPrivateKeyDemoPrivateKey0=\nAddress = ${peer.address}\n\n[Peer]\nPublicKey = ${settings.wireguard.public_key}\nEndpoint = ${settings.wireguard.endpoint}\nAllowedIPs = 10.99.0.0/24\nPersistentKeepalive = 25\n`;
    return { status: 200, body: { settings, config } };
  },
  "/storage/pools": (h, c) => ({ status: 201, body: createPool(h, c.body) }),
  "/storage/pools/{uuid}/disks": (h, c) => (addPoolDisk(h, c.path.uuid!, c.body), { status: 204 }),
  "/system/power-meter/test": (h, c) => {
    const entity = String(c.body.entity ?? "");
    if (entity !== "sensor.server_plug_power") return { status: 200, body: { ok: false, code: "entity_not_found", error: `Home Assistant has no ${entity}`, watts: null } };
    return { status: 200, body: { ok: true, name: "Server plug Power", watts: powerMeter(h).watts ?? 23.4 } };
  },
};

/** The puts it acts out: disabling a stack's service. */
const puts: Record<string, (h: Host, c: Ctx) => { status: number; body?: unknown }> = {
  "/services/{unit}/mask": (h, c) => (maskService(h, c.path.unit!, true), { status: 204 }),
  "/system/power-profile": (h, c) => {
    const p = String(c.body.profile);
    if (p !== "power-saver" && p !== "balanced" && p !== "performance") throw new DemoProblem(400, "invalid_setting", "profile: power-saver, balanced or performance");
    h.profile = { profile: p, governor: String(c.body.governor ?? "") };
    return { status: 200, body: powerProfile(h) };
  },
  "/system/power-meter": (h, c) => {
    const url = String(c.body.url ?? "").trim();
    const entity = String(c.body.entity ?? "").trim();
    const token = c.body.token === undefined ? undefined : String(c.body.token);
    const has = url === "" ? false : token === undefined ? h.meter.has_token : token !== "";
    if (url !== "" && !has) throw new DemoProblem(422, "token_required", "a long-lived access token from Home Assistant is needed");
    h.meter = { url, entity, has_token: has, idle_only: c.body.idle_only === true };
    return { status: 200, body: powerMeter(h) };
  },
};

/** The deletes it acts out: a pool, a pool's disk, a removal that is stopped, and enabling a disabled service. */
const deletes: Record<string, (h: Host, c: Ctx) => { status: number; body?: unknown }> = {
  "/services/{unit}/mask": (h, c) => (maskService(h, c.path.unit!, false), { status: 204 }),
  "/storage/pools/{uuid}": (h, c) => (deletePool(h, c.path.uuid!, c.query.get("confirm")), { status: 204 }),
  "/storage/pools/{uuid}/disks": (h, c) => ({ status: 202, body: removePoolDisk(h, c.path.uuid!, c.query.get("disk") ?? "") }),
  "/storage/pools/{uuid}/operation": (h, c) => (endRemoval(h, c.path.uuid!), { status: 204 }),
};

interface Route {
  re: RegExp;
  names: string[];
  run: (h: Host, c: Ctx) => { status: number; body?: unknown };
}

function compile(table: Record<string, (h: Host, c: Ctx) => unknown>, wrap: (v: unknown) => { status: number; body?: unknown }): Route[] {
  return Object.entries(table)
    .sort(([a], [b]) => (a.match(/{/g)?.length ?? 0) - (b.match(/{/g)?.length ?? 0) || b.length - a.length)
    .map(([pattern, fn]) => {
      const names: string[] = [];
      const re = new RegExp("^" + pattern.replace(/{(\w+)}/g, (_, n: string) => (names.push(n), "([^/]+)")) + "$");
      return { re, names, run: (h, c) => wrap(fn(h, c)) };
    });
}

const getRoutes = compile(gets, (body) => ({ status: 200, body }));
const postRoutes = compile(posts, (v) => v as { status: number; body?: unknown });
const putRoutes = compile(puts, (v) => v as { status: number; body?: unknown });
const deleteRoutes = compile(deletes, (v) => v as { status: number; body?: unknown });

const json = (status: number, body: unknown, type = "application/json") => new Response(JSON.stringify(body), { status, headers: { "Content-Type": type } });
const problem = (status: number, code: string, detail: string) => json(status, { type: "about:blank", title: detail, status, code, detail }, "application/problem+json");

/** A fetch that answers from a demo host. */
export function demoFetchFor(h: Host) {
  return async (input: Request): Promise<Response> => {
    const url = new URL(input.url);
    const path = url.pathname.replace(/^\/api/, "");
    const get = input.method === "GET";
    tick(h);
    // A host that reboots answers nothing, as a browser sees it.
    if (h.now() < h.downUntil) throw new TypeError("Failed to fetch");
    for (const r of get ? getRoutes : input.method === "POST" ? postRoutes : input.method === "PUT" ? putRoutes : input.method === "DELETE" ? deleteRoutes : []) {
      const m = r.re.exec(path);
      if (!m) continue;
      const ctx: Ctx = { path: Object.fromEntries(r.names.map((n, i) => [n, decodeURIComponent(m[i + 1]!)])), query: url.searchParams, body: {} };
      if (!get) ctx.body = (await input.json().catch(() => ({}))) as Record<string, unknown>;
      try {
        const out = r.run(h, ctx);
        // A copy: the page must not hold the host's own objects.
        return out.body === undefined ? new Response(null, { status: out.status }) : json(out.status, out.body);
      } catch (e) {
        if (e instanceof DemoProblem) return problem(e.status, e.code, e.message);
        throw e;
      }
    }
    if (get) return problem(404, "not_found", "The demo has nothing here.");
    return problem(403, "not_in_demo", "Not in the demo. Nothing was changed.");
  };
}

/** The one host a visit to the demo runs on. */
export const host = newHost();
export const demoFetch = demoFetchFor(host);
