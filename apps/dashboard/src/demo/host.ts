/**
 * The demo's host: one made-up home server, held in memory. Every page of
 * the demo reads it through the fake API (fetch.ts), and four flows change
 * it: approving a stack's grants, updating and rolling back a stack, running
 * a backup, and installing an image update. A reload starts it again.
 *
 * Nothing here is a real host's: the domain is home.example and the
 * addresses are from the documentation ranges.
 */
import type { Schema } from "#lib/api/client.ts";

type Stack = Schema<"Stack">;
type Manifest = Schema<"StackManifest">;
type Backup = Schema<"Backup">;
type BackupRun = Schema<"BackupRun">;
type Disk = Schema<"Disk">;

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;
// The dashboard counts in powers of ten, as disk makers do.
const GB = 1e9;
const TB = 1e12;

/** A step that takes time: it is applied by the first read after `at`. */
interface Pending {
  at: number;
  apply: () => void;
}

export interface Host {
  now: () => number;
  stacks: Stack[];
  services: Schema<"ServiceInfo">[];
  updates: Schema<"StackUpdate">[];
  backups: Backup[];
  destinations: Schema<"Destination">[];
  runs: Schema<"BackupRunDetail">[];
  image: Schema<"ImageStatus">;
  deploys: Schema<"CoreDeploy">[];
  storage: Schema<"StorageStatus">;
  power: Schema<"PowerStatus">;
  /** Until when the host is down (ms): every request fails, as when it reboots. */
  downUntil: number;
  pending: Pending[];
}

const rev = (seed: string) =>
  Array.from({ length: 40 }, (_, i) => ((seed.charCodeAt(i % seed.length) * (i + 7)) % 16).toString(16)).join("");

function manifest(name: string, m: Partial<Manifest>): Manifest {
  return {
    name,
    version: 8,
    nixpkgs: "nixos-26.05",
    containers: false,
    gpu: false,
    egress: [],
    ports: [],
    routes: [],
    storage: [],
    health: { units: [name] },
    ...m,
  };
}

interface StackSeed {
  name: string;
  label: string;
  units: string[];
  cpu: number;
  memory: number;
  generation: number;
  manifest: Partial<Manifest>;
  variables?: Schema<"StackVariable">[];
}

const pathVar = (name: string, value: string, description: string): Schema<"StackVariable"> => ({
  name,
  kind: "path",
  description,
  declared: true,
  required: true,
  secret: false,
  in_env: true,
  set: true,
  source: "stored",
  value,
  mount: `/data/${name.toLowerCase()}`,
});

const secretVar = (name: string, description: string): Schema<"StackVariable"> => ({
  name,
  kind: "secret",
  description,
  declared: true,
  required: true,
  secret: true,
  in_env: false,
  set: true,
  source: "stored",
});

const SEEDS: StackSeed[] = [
  {
    name: "photos",
    label: "Photos",
    units: ["immich", "postgres", "redis"],
    cpu: 3.1,
    memory: 2.4 * GB,
    generation: 14,
    manifest: {
      routes: [{ host: "photos", port: 2283, max_body: "50G" }],
      gpu: true,
      backup: { prepare: "immich-dump", folders: [{ label: "Library", variable: "LIBRARY" }, { label: "Database dump", path: "/var/backup" }] },
      health: { units: ["immich"], http: { path: "/api/server/ping", port: 2283 } },
    },
    variables: [pathVar("LIBRARY", "/var/mnt/tank/photos", "Where the photo library is kept."), secretVar("DB_PASSWORD", "The database's password.")],
  },
  {
    name: "media",
    label: "Media",
    units: ["jellyfin"],
    cpu: 6.4,
    memory: 1.1 * GB,
    generation: 9,
    manifest: {
      routes: [{ host: "media", port: 8096 }],
      gpu: true,
      health: { units: ["jellyfin"], http: { path: "/health", port: 8096 } },
    },
    variables: [pathVar("MEDIA", "/var/mnt/tank/media", "Films, series and music.")],
  },
  {
    name: "documents",
    label: "Documents",
    units: ["paperless", "postgres", "redis"],
    cpu: 0.8,
    memory: 900 * 1024 ** 2,
    generation: 6,
    manifest: {
      routes: [{ host: "documents", port: 8000 }],
      backup: { folders: [{ label: "Documents", variable: "DOCUMENTS" }] },
    },
    variables: [pathVar("DOCUMENTS", "/var/mnt/tank/documents", "Scanned and archived documents."), secretVar("SECRET_KEY", "Signs sessions.")],
  },
  {
    name: "home",
    label: "Home",
    units: ["home-assistant", "mosquitto"],
    cpu: 1.9,
    memory: 700 * 1024 ** 2,
    generation: 21,
    manifest: {
      routes: [{ host: "home", port: 8123 }],
      ports: [{ port: 1883, proto: "tcp", to: 1883, from: ["$IOT"] }],
      api: ["read"],
      backup: { folders: [{ label: "Configuration", path: "/var/lib/hass" }] },
    },
    variables: [
      { name: "IOT", kind: "address", description: "The devices that may reach MQTT.", declared: true, required: true, secret: false, in_env: false, set: true, source: "stored", value: "192.0.2.0/26" },
    ],
  },
  {
    name: "downloads",
    label: "Downloads",
    units: ["sonarr", "radarr", "prowlarr", "qbittorrent"],
    cpu: 2.2,
    memory: 1.3 * GB,
    generation: 11,
    manifest: {
      routes: [
        { host: "series", port: 8989 },
        { host: "films", port: 7878 },
      ],
      egress: [{ to: "0.0.0.0/0", port: 443, proto: "tcp" }],
    },
    variables: [pathVar("MEDIA", "/var/mnt/tank/media", "Where finished downloads go.")],
  },
  {
    name: "git",
    label: "Git",
    units: ["forgejo"],
    cpu: 0.4,
    memory: 310 * 1024 ** 2,
    generation: 4,
    manifest: {
      routes: [{ host: "git", port: 3000 }],
      ports: [{ port: 2222, proto: "tcp", to: 22, from: ["192.0.2.0/24"] }],
      backup: { folders: [{ label: "Repositories", path: "/var/lib/forgejo" }] },
    },
  },
];

const share = (k: number, n: number) => (n === 1 ? 1 : k === 0 ? 0.64 : 0.36 / (n - 1));

export const iso = (t: number) => new Date(t).toISOString();

function seedStack(s: StackSeed, i: number, now: number): Stack {
  const m = manifest(s.name, s.manifest);
  const at = now - (i + 2) * 3 * DAY;
  const approved = { at: iso(at), manifest: m, rev: rev(s.name), url: `https://git.home.example/stacks/${s.name}` };
  return {
    name: s.name,
    label: s.label,
    address: `10.88.${i + 1}.2`,
    source: `https://git.home.example/stacks/${s.name}`,
    source_dirty: false,
    created_at: iso(now - (120 - i * 11) * DAY),
    approved,
    fetched: approved,
    commits: [],
    generation: s.generation,
    generations: [0, 1, 2].filter((k) => s.generation - k > 0).map((k) => ({
      id: s.generation - k,
      built_at: iso(at - k * 9 * DAY),
      failed: false,
      rev: k === 0 ? approved.rev : rev(s.name + k),
      nixpkgs_rev: rev("nixpkgs" + k),
    })),
    new_grants: [],
    nixpkgs: "nixos-26.05",
    nixpkgs_rev: rev("nixpkgs0"),
    restart_needed: false,
    idle_grants: [],
    running: true,
    want_running: true,
    autostart: true,
    frozen: false,
    update_state: "up_to_date",
    usage: { cpu_percent: s.cpu, memory_bytes: Math.round(s.memory) },
    folders: [],
    variables: s.variables ?? [],
    variables_ready: true,
  };
}

function disk(name: string, model: string, size: number, label: string, health: Partial<Schema<"DiskHealth">>, now: number): Disk {
  return {
    name,
    path: `/dev/${name}`,
    model,
    serial: `DEMO${name.toUpperCase()}0${size % 97}`,
    size,
    type: "disk",
    fstype: "bcachefs",
    label: "tank",
    bcachefs_label: label,
    friendly_name: model,
    mountpoint: "",
    data_target: label.split(".")[0] ?? "",
    metadata_target: "ssd",
    kind: label.startsWith("ssd") ? "ssd" : "hdd",
    contents: "",
    state: "rw",
    health: { bad_sectors: 0, checked_at: iso(now - 20 * MIN), passed: true, power_on_hours: 11200, temperature_c: 34, ...health },
  };
}

function run(id: number, b: { id: number; name: string }, startedAgo: number, took: number, bytes: number, now: number, result: BackupRun["result"] = "success"): BackupRun {
  return {
    id,
    backup_id: b.id,
    backup_name: b.name,
    bytes,
    files: Math.round(bytes / (3.2 * 1024 ** 2)),
    error: result === "failure" ? "the destination did not answer: dial tcp: i/o timeout" : "",
    started_at: iso(now - startedAgo),
    finished_at: iso(now - startedAgo + took),
    result,
    trigger: "cron",
  };
}

function seedBackups(now: number): { backups: Backup[]; destinations: Schema<"Destination">[] } {
  const keep = { daily: 7, weekly: 4, monthly: 12, yearly: 2 };
  const destinations: Schema<"Destination">[] = [
    {
      id: 1,
      name: "Offsite",
      kind: "restic",
      keep,
      delete_removed: false,
      password_confirmed: true,
      restic: { repository: "s3:s3.eu-central-003.example/home-backup", env_names: ["AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"] },
      used_by: ["Photos", "Documents and git"],
      created_at: iso(now - 110 * DAY),
      updated_at: iso(now - 30 * DAY),
    },
    {
      id: 2,
      name: "NAS in the cellar",
      kind: "rclone",
      keep,
      delete_removed: true,
      password_confirmed: true,
      rclone: { remote: "cellar", type: "sftp", path: "backups/homelab" },
      used_by: ["Photos", "Core"],
      created_at: iso(now - 90 * DAY),
      updated_at: iso(now - 90 * DAY),
    },
  ];
  const ref = (id: number) => {
    const d = destinations.find((x) => x.id === id)!;
    return { id: d.id, kind: d.kind, name: d.name, ready: true };
  };
  const source = (stack: string, label: string, path: string): Schema<"Source"> => ({
    kind: "stack",
    stack,
    folder: label.toLowerCase().replaceAll(" ", "-"),
    label,
    host_path: path,
    path,
    problem: "",
    remote_path: null,
    size_only: false,
    declared_excludes: [],
    excludes: [],
  });
  const make = (id: number, name: string, schedule: string, dests: number[], sources: Schema<"Source">[], bytes: number, lastAgo: number, failed = false): Backup => {
    const recent = [0, 1, 2, 3, 4].map((k) => run(id * 100 - k, { id, name }, lastAgo + k * DAY, (6 + k) * MIN, bytes, now, failed && k === 0 ? "failure" : "success"));
    return {
      id,
      name,
      schedule,
      enabled: true,
      bwlimit: "",
      healthcheck_url: "",
      transfers: 4,
      destinations: dests.map(ref),
      sources,
      running: false,
      overdue: false,
      last_run: recent[0] ?? null,
      last_success: failed ? recent[1]!.finished_at : recent[0]!.finished_at,
      next_run: iso(now - lastAgo + DAY),
      recent_runs: recent,
      created_at: iso(now - 100 * DAY),
      updated_at: iso(now - 12 * DAY),
    };
  };
  const core: Schema<"Source"> = { ...source("", "Core", "/var/lib/homelab"), kind: "core" };
  return {
    destinations,
    backups: [
      make(1, "Photos", "0 2 * * *", [1, 2], [source("photos", "Library", "/var/mnt/tank/photos"), source("photos", "Database dump", "/var/backup")], 2.4 * GB, 9 * HOUR),
      make(2, "Documents and git", "30 2 * * *", [1], [source("documents", "Documents", "/var/mnt/tank/documents"), source("git", "Repositories", "/var/lib/forgejo")], 310 * 1024 ** 2, 8.5 * HOUR),
      make(3, "Core", "0 3 * * *", [2], [core], 48 * 1024 ** 2, 8 * HOUR),
      make(4, "Media, weekly", "0 4 * * 0", [2], [{ ...source("media", "Media", "/var/mnt/tank/media"), kind: "path", stack: "" }], 38 * GB, 3 * DAY, true),
    ],
  };
}

/** A fresh host. `now` is the clock, so a test can move time. */
export function newHost(now: () => number = Date.now): Host {
  const t = now();
  const stacks = SEEDS.map((s, i) => seedStack(s, i, t));

  // Media has two commits waiting: an update to install.
  const media = stacks.find((s) => s.name === "media")!;
  media.update_state = "commits_available";
  media.commits = [
    { rev: rev("media-next"), at: iso(t - 5 * HOUR), subject: "jellyfin 10.11.4" },
    { rev: rev("media-mid"), at: iso(t - 2 * DAY), subject: "hardware decoding: use the render node the host passes in" },
  ];
  media.fetched = { ...media.approved!, rev: media.commits[0]!.rev, at: media.commits[0]!.at };
  media.approved = media.fetched;

  // Home's newest commit asks for one more port: a grant to approve.
  const home = stacks.find((s) => s.name === "home")!;
  const asked = manifest("home", {
    ...home.approved!.manifest,
    ports: [...home.approved!.manifest.ports, { port: 5353, proto: "udp", to: 5353, from: ["$IOT"] }],
  });
  home.update_state = "needs_approval";
  home.commits = [{ rev: rev("home-next"), at: iso(t - 26 * HOUR), subject: "mDNS for device discovery" }];
  home.fetched = { ...home.approved!, rev: home.commits[0]!.rev, at: home.commits[0]!.at, manifest: asked };
  home.new_grants = [{ kind: "port", detail: "5353/udp from $IOT" }];

  // The operator disabled one of Downloads' services.
  const disabled = (stack: string, u: string) => stack === "downloads" && u === "prowlarr";
  const services = SEEDS.flatMap((s, i) =>
    s.units.map((u, k) => ({
      name: u,
      unit_name: `${s.name}-${u}.service`,
      description: `${s.label}: ${u}`,
      stack: s.name,
      type: "Native" as const,
      masked: disabled(s.name, u),
      active_state: disabled(s.name, u) ? "inactive" : "active",
      sub_state: disabled(s.name, u) ? "dead" : "running",
      started_at: disabled(s.name, u) ? null : iso(t - (i + 2) * 3 * DAY + k * 4000),
      // The first unit is the service itself; the rest are its helpers.
      ...(disabled(s.name, u) ? {} : { usage: { cpu_percent: +(s.cpu * share(k, s.units.length)).toFixed(1), memory_bytes: Math.round(s.memory * share(k, s.units.length)) } }),
    })),
  );

  const updates = stacks.flatMap((s, i) =>
    s.generations.map((g, k) => ({
      id: 1000 - i * 10 - k,
      stack: s.name,
      kind: (g.id === 1 ? "install" : "update") as "install" | "update",
      result: "healthy" as const,
      reason: "",
      rev: g.rev,
      from_generation: g.id - 1,
      to_generation: g.id,
      started_at: iso(Date.parse(g.built_at) - 3 * MIN),
      finished_at: g.built_at,
      changes: k === 0 ? [{ kind: "upgrade" as const, name: s.name, from: "1.4.2", to: "1.5.0" }] : [],
    })),
  );

  const { backups, destinations } = seedBackups(t);

  const disks = [
    disk("sda", "WDC WD120EFBX", 12 * TB, "hdd.a", { power_on_hours: 18400, temperature_c: 36 }, t),
    disk("sdb", "ST8000VN004", 8 * TB, "hdd.b", { power_on_hours: 31700, temperature_c: 39, bad_sectors: 8 }, t),
    disk("nvme1n1", "Samsung SSD 990 PRO 1TB", 1 * TB, "ssd.a", { power_on_hours: 6100, temperature_c: 44 }, t),
  ];
  // Raw figures: with two copies of each file, half of this is usable.
  const used = 9.6 * TB;
  const total = 21 * TB;
  // A second pool of two fast disks, mirrored, for databases and app data.
  const fast = [
    disk("nvme2n1", "WD Black SN850X 2TB", 2 * TB, "nvme.a", { power_on_hours: 4200, temperature_c: 47 }, t),
    disk("nvme3n1", "WD Black SN850X 2TB", 2 * TB, "nvme.b", { power_on_hours: 4200, temperature_c: 46 }, t),
  ];
  const fastUsed = 1.1 * TB;

  return {
    now,
    stacks,
    services,
    updates,
    backups,
    destinations,
    runs: [],
    image: {
      booted: "0.2.5",
      default: "0.2.5",
      source: "release",
      pending_reboot: false,
      available: [],
      installed: [
        { version: "0.2.5", state: "booted" },
        { version: "0.2.2", state: "good" },
      ],
      core: { active: "0.2.5", available: [], installed: ["0.2.5", "0.2.2"] },
      release: { latest: "0.2.6", url: "https://dl.homelab-ngx.com", checked_at: iso(t - 7 * HOUR) },
    },
    deploys: [
      { id: 12, kind: "install", version: "0.2.5", rev: "", phase: "Health check", reason: "", result: "healthy", log: "", failed_units: [], started_at: iso(t - 6 * DAY), finished_at: iso(t - 6 * DAY + 2 * MIN) },
      { id: 11, kind: "download", version: "0.2.5", rev: "", phase: "Stage", reason: "", result: "healthy", log: "", failed_units: [], started_at: iso(t - 6 * DAY - HOUR), finished_at: iso(t - 6 * DAY - HOUR + 4 * MIN) },
    ],
    storage: {
      pools: [
        {
          name: "tank",
          label: "tank",
          uuid: "5f0c1a52-7d3e-4b8a-9c61-2e4d8a7b1c90",
          state: "mounted",
          mountdir: "/var/mnt/tank",
          mount_name: "tank",
          auto_mount: true,
          data_replicas: 2,
          metadata_replicas: 2,
          foreground_target: "ssd",
          promote_target: "ssd",
          background_target: "hdd",
          metadata_target: "ssd",
          disks,
          usage: { total_bytes: total, used_bytes: used, available_bytes: total - used, used_percent: (used / total) * 100 },
        },
        {
          name: "apps",
          label: "apps",
          uuid: "a3e9b7d4-1c2f-4e6a-8b5d-7f0c9e1a2b34",
          state: "mounted",
          mountdir: "/var/mnt/apps",
          mount_name: "apps",
          auto_mount: true,
          data_replicas: 2,
          metadata_replicas: 2,
          foreground_target: "",
          promote_target: "",
          background_target: "",
          metadata_target: "",
          disks: fast,
          usage: { total_bytes: 4 * TB, used_bytes: fastUsed, available_bytes: 4 * TB - fastUsed, used_percent: (fastUsed / (4 * TB)) * 100 },
        },
      ],
      subvolumes: [
        { id: "256", path: "/var/mnt/tank/photos", used_bytes: 1.1 * TB, measured_at: iso(t - HOUR) },
        { id: "257", path: "/var/mnt/tank/media", used_bytes: 3.4 * TB, measured_at: iso(t - HOUR) },
        { id: "258", path: "/var/mnt/tank/documents", used_bytes: 41 * GB, measured_at: iso(t - HOUR) },
        { id: "259", path: "/var/mnt/tank/scratch", used_bytes: 220 * GB, measured_at: iso(t - HOUR) },
        { id: "256", path: "/var/mnt/apps/databases", used_bytes: 310 * GB, measured_at: iso(t - HOUR) },
        { id: "257", path: "/var/mnt/apps/appdata", used_bytes: 180 * GB, measured_at: iso(t - HOUR) },
      ],
      unassigned: [
        { ...disk("sdc", "ST8000VN004", 8 * TB, "", { power_on_hours: 120, temperature_c: 31 }, t), fstype: "", label: "", state: "", data_target: "", metadata_target: "" },
        { ...disk("sdd", "Crucial MX500 500GB", 500 * GB, "", { power_on_hours: 9800, temperature_c: 33 }, t), kind: "ssd", fstype: "", label: "", state: "", data_target: "", metadata_target: "", has_children: true, contents: "2 partitions" },
      ],
      partitions: [
        {
          uuid: "5b1d2c0e-7a44-4d0b-9c1f-2f8e6a3b9d10",
          path: "/dev/sdf1",
          disk: "Samsung SSD 870 EVO 1TB",
          fstype: "ext4",
          label: "scratch",
          size: 1000 * GB,
          state: "mounted",
          mountdir: "/var/mnt/scratch",
          mount_name: "scratch",
          auto_mount: true,
          read_only: false,
          usage: { total: 984 * GB, free: 610 * GB },
        },
        {
          uuid: "64A1-9C3E",
          path: "/dev/sdg1",
          disk: "WD Elements 2TB",
          fstype: "exfat",
          label: "Holiday 2025",
          size: 2000 * GB,
          state: "unmounted",
          mountdir: "",
          mount_name: "holiday-2025",
          auto_mount: false,
          read_only: false,
        },
      ],
    },
    power: idlePower("demo-boot-1"),
    downUntil: 0,
    pending: [],
  };
}

const REBOOT_SECONDS = 25;

const idlePower = (boot: string): Schema<"PowerStatus"> => ({ phase: "idle", steps: [], busy: [], frozen: [], boot_id: boot, expected_seconds: REBOOT_SECONDS });

/** The power status as the daemon reports it: the frozen stacks are read now. */
export function powerStatus(h: Host): Schema<"PowerStatus"> {
  return { ...h.power, frozen: h.power.phase === "idle" ? h.stacks.filter((s) => s.frozen).map((s) => s.name) : [] };
}

/**
 * A reboot or poweroff as the daemon runs it: the running stacks stop, then
 * the pools and partitions unmount, then the host is gone. A reboot comes
 * back from a new boot; a poweroff stays off until the page is reloaded.
 */
export function power(h: Host, action: "reboot" | "poweroff", force: boolean, thaw = false) {
  if (h.power.phase !== "idle") throw new DemoProblem(409, "shutting_down", "The host is already shutting down.");
  const working = h.stacks.filter(busy).map((s) => `stack ${s.name}: ${s.operation!.kind} (${s.operation!.phase})`);
  if (working.length > 0 && !force) throw new DemoProblem(409, "busy", `${working.join(", ")} would be interrupted; force to ${action} anyway`);
  const frozen = h.stacks.filter((s) => s.frozen).map((s) => s.name);
  if (frozen.length > 0 && !force && !thaw)
    throw new DemoProblem(409, "stacks_frozen", `frozen stacks lose what they hold: ${frozen.join(", ")}; thaw them first, or force to ${action} without thawing`);
  for (const s of h.stacks) s.frozen = false;
  const mounts = [...h.storage.pools.map((p) => p.mountdir), ...h.storage.partitions.filter((p) => p.state === "mounted").map((p) => p.mountdir)];
  h.power = {
    ...h.power,
    action,
    phase: "stopping_stacks",
    started_at: iso(h.now()),
    steps: h.stacks.filter((s) => s.running).map((s) => ({ kind: "stack", name: s.name, state: "working" })),
  };
  const set = (kind: "stack" | "mount", state: "working" | "done") => {
    for (const st of h.power.steps) if (st.kind === kind) st.state = state;
  };
  later(h, 3000, () => {
    set("stack", "done");
    h.power.phase = "unmounting";
    h.power.steps.push(...mounts.map((m) => ({ kind: "mount" as const, name: m, state: "working" as const })));
  });
  later(h, 5000, () => {
    set("mount", "done");
    h.power.phase = "going_down";
  });
  // The daemon keeps the host up until a page has read that it is ready.
  later(h, 7000, () => {
    if (action === "poweroff") {
      h.downUntil = Infinity;
      return;
    }
    h.downUntil = h.now() + REBOOT_SECONDS * 1000 - 2000;
    reboot(h);
    h.power = idlePower(`demo-boot-${Math.round(h.now())}`);
  });
  return h.power;
}

/** Applies every step whose time has come. The fake API calls it before each request. */
export function tick(h: Host) {
  const t = h.now();
  const due = h.pending.filter((p) => p.at <= t).sort((a, b) => a.at - b.at);
  h.pending = h.pending.filter((p) => p.at > t);
  for (const p of due) p.apply();
}

const later = (h: Host, ms: number, apply: () => void) => h.pending.push({ at: h.now() + ms, apply });

export class DemoProblem extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    detail: string,
  ) {
    super(detail);
  }
}

export function stackOf(h: Host, name: string): Stack {
  const s = h.stacks.find((x) => x.name === name);
  if (!s) throw new DemoProblem(404, "stack_not_found", `There is no stack ${name}.`);
  return s;
}

const busy = (s: Stack) => !!s.operation && !s.operation.finished_at;

function setRunning(h: Host, s: Stack, running: boolean) {
  s.running = running;
  for (const u of h.services.filter((x) => x.stack === s.name && !x.masked)) {
    u.active_state = running ? "active" : "inactive";
    u.sub_state = running ? "running" : "dead";
    u.started_at = running ? iso(h.now()) : null;
  }
}

/** A stack's service disabled (stopped now, kept stopped) or enabled again (not started). */
export function maskService(h: Host, unit: string, on: boolean) {
  const u = h.services.find((x) => x.unit_name === unit || x.unit_name === `${unit}.service`);
  if (!u?.stack) throw new DemoProblem(404, "unknown_unit", `No stack has a service unit named "${unit}".`);
  if (on && !u.masked && h.services.filter((x) => x.stack === u.stack && !x.masked).length <= 1)
    throw new DemoProblem(409, "last_unit", `${unit} is the last enabled service of stack "${u.stack}"; stop the stack instead.`);
  u.masked = on;
  if (on) {
    Object.assign(u, { active_state: "inactive", sub_state: "dead", started_at: null });
    delete u.usage;
  }
}

/** Flow 1: the fetched commit's grants are approved. */
export function approve(h: Host, name: string, at: string): Stack {
  const s = stackOf(h, name);
  if (at !== s.fetched.rev) throw new DemoProblem(409, "stale_revision", "The stack has a newer commit. Review it again.");
  s.approved = s.fetched;
  s.new_grants = [];
  s.update_state = s.generations.some((g) => g.rev === s.fetched.rev) ? "up_to_date" : "commits_available";
  return s;
}

function build(h: Host, s: Stack, kind: "update" | "rollback", to: { id: number; rev: string }, done: () => void) {
  if (busy(s)) throw new DemoProblem(409, "stack_busy", `${s.label} is already changing.`);
  const id = Math.max(0, ...h.updates.map((u) => u.id)) + 1;
  const from = s.generation;
  s.operation = { kind, phase: kind === "update" ? "Building" : "Switching", started_at: iso(h.now()) };
  h.updates.unshift({ id, stack: s.name, kind, result: "running", reason: "", rev: to.rev, from_generation: from, to_generation: to.id, started_at: iso(h.now()), changes: [] });
  later(h, kind === "update" ? 3000 : 1000, () => (s.operation = { ...s.operation!, phase: "Health check" }));
  later(h, kind === "update" ? 6000 : 3000, () => {
    done();
    s.operation = undefined;
    const u = h.updates.find((x) => x.id === id)!;
    u.result = "healthy";
    u.finished_at = iso(h.now());
    if (kind === "update") u.changes = [{ kind: "upgrade", name: s.name, from: "the version before", to: s.commits[0]?.subject ?? "the approved commit" }];
  });
}

/** Flow 2: the approved commit is built and switched to. */
export function update(h: Host, name: string): Stack {
  const s = stackOf(h, name);
  if (!s.approved || s.generations.some((g) => g.id === s.generation && g.rev === s.approved!.rev)) {
    throw new DemoProblem(409, "nothing_to_update", `${s.label} already runs the approved commit.`);
  }
  const next = { id: Math.max(...s.generations.map((g) => g.id)) + 1, rev: s.approved.rev };
  build(h, s, "update", next, () => {
    s.generations.unshift({ id: next.id, rev: next.rev, built_at: iso(h.now()), failed: false, nixpkgs_rev: s.nixpkgs_rev });
    s.generation = next.id;
    s.commits = [];
    s.update_state = s.new_grants.length ? "needs_approval" : "up_to_date";
  });
  return s;
}

/** Flow 2, back: an earlier generation is switched to. */
export function rollback(h: Host, name: string, generation: number): Stack {
  const s = stackOf(h, name);
  const g = s.generations.find((x) => x.id === generation);
  if (!g || g.id === s.generation) throw new DemoProblem(422, "no_such_generation", `${s.label} has no other version ${generation}.`);
  build(h, s, "rollback", g, () => (s.generation = g.id));
  return s;
}

/** Freeze or thaw a running stack. */
export function freezeThaw(h: Host, name: string, frozen: boolean): Stack {
  const s = stackOf(h, name);
  if (!s.running) throw new DemoProblem(409, "stack_not_running", `stack "${name}" is not running`);
  if (busy(s)) throw new DemoProblem(409, "busy", `stack "${name}" is busy`);
  s.frozen = frozen;
  return s;
}

export function startStop(h: Host, name: string, running: boolean): Stack {
  const s = stackOf(h, name);
  if (running && s.frozen) throw new DemoProblem(409, "stack_frozen", `stack "${name}" is frozen; thaw it first`);
  s.frozen = false;
  s.want_running = running;
  setRunning(h, s, running);
  return s;
}

/** Flow 3: a backup runs, and its run is in the history. */
export function runBackup(h: Host, id: number): { run_id: number } {
  const b = h.backups.find((x) => x.id === id);
  if (!b) throw new DemoProblem(404, "backup_not_found", "There is no such backup.");
  if (b.running) throw new DemoProblem(409, "backup_running", `${b.name} is already running.`);
  const runId = Math.max(...h.backups.flatMap((x) => x.recent_runs.map((r) => r.id)), 0) + 1;
  const size = b.recent_runs[0]?.bytes ?? 100 * 1024 ** 2;
  const r: BackupRun = { id: runId, backup_id: b.id, backup_name: b.name, bytes: 0, files: 0, error: "", started_at: iso(h.now()), finished_at: null, result: "running", trigger: "manual" };
  const detail: Schema<"BackupRunDetail"> = {
    ...r,
    progress: { bytes_done: 0, bytes_total: size, files_done: 0, files_total: Math.round(size / (3.2 * 1024 ** 2)), destination: b.destinations[0]?.name ?? "", folder: b.sources[0]?.label ?? "", speed: 0 },
    items: b.sources.map((s) => ({ kind: "folder" as const, label: s.label, path: s.host_path, destination: b.destinations[0]?.name ?? "", bytes: 0, files: 0, error: "", note: "", result: "pending" as const, started_at: null, finished_at: null })),
  };
  b.running = true;
  b.last_run = r;
  b.recent_runs.unshift(r);
  h.runs.unshift(detail);
  later(h, 4000, () => {
    detail.progress = { ...(detail.progress as Schema<"BackupProgress">), bytes_done: Math.round(size * 0.6), speed: 42 * 1024 ** 2 };
    detail.bytes = r.bytes = Math.round(size * 0.6);
  });
  later(h, 8000, () => {
    const end = iso(h.now());
    Object.assign(r, { bytes: size, files: Math.round(size / (3.2 * 1024 ** 2)), finished_at: end, result: "success" });
    Object.assign(detail, r, { progress: null });
    detail.items = detail.items.map((i) => ({ ...i, result: "success" as const, bytes: Math.round(size / detail.items.length), finished_at: end, started_at: r.started_at }));
    b.running = false;
    b.last_success = end;
  });
  return { run_id: runId };
}

function deploy(h: Host, kind: Schema<"CoreDeploy">["kind"], version: string, phases: string[], done: () => void): Schema<"CoreDeploy"> {
  if (h.deploys[0]?.result === "running") throw new DemoProblem(409, "deploy_running", "An update is already running.");
  const d: Schema<"CoreDeploy"> = { id: (h.deploys[0]?.id ?? 0) + 1, kind, version, rev: "", phase: phases[0]!, reason: "", result: "running", log: "", failed_units: [], started_at: iso(h.now()), finished_at: null };
  h.deploys.unshift(d);
  phases.slice(1).forEach((p, i) => later(h, (i + 1) * 1500, () => (d.phase = p)));
  later(h, phases.length * 1500, () => {
    done();
    d.result = "healthy";
    d.finished_at = iso(h.now());
  });
  return d;
}

/** Flow 4, first half: the newest release is downloaded and staged. */
export function downloadImage(h: Host): Schema<"CoreDeploy"> {
  const v = h.image.release.latest;
  const have = h.image.installed.some((x) => x.version === v) || h.image.available.includes(v);
  return deploy(h, "download", have ? "" : v, have ? ["Check"] : ["Check", "Verify", "Download", "Stage"], () => {
    h.image.release.checked_at = iso(h.now());
    if (have) return;
    h.image.available = [v];
    h.image.core.available = [v];
  });
}

/**
 * Flow 4, second half: the staged version goes into the free slot and waits
 * for a reboot, or with reboot "now" the host reboots into it.
 */
export function installImage(h: Host, reboot = ""): Schema<"CoreDeploy"> {
  const v = h.image.available[0];
  if (!v) throw new DemoProblem(409, "nothing_staged", "No downloaded version is waiting.");
  const d = deploy(h, "install", v, ["Update", "Restart", "Health check"], () => {
    h.image.available = [];
    h.image.core = { ...h.image.core, active: v, available: [], installed: [v, ...h.image.core.installed] };
    h.image.installed = [{ version: v, state: "pending", tries_left: 3 }, ...h.image.installed.filter((x) => x.state === "booted")];
    h.image.default = v;
    h.image.pending_reboot = true;
    if (reboot !== "now") return;
    d.phase = `Installed ${v}; rebooting`;
    later(h, 500, () => power(h, "reboot", true, true));
  });
  // The new core restarts the daemon: for a moment nothing answers.
  const restart = h.now() + 1500;
  later(h, 1500, () => (h.downUntil = Math.max(h.downUntil, restart + 2500)));
  return d;
}

/** The demo's host restarts at once: the pending version is the booted one. */
export function reboot(h: Host) {
  const next = h.image.installed.find((x) => x.state === "pending");
  if (!next) return;
  h.image.installed = h.image.installed.map((x) => (x === next ? { version: x.version, state: "booted" } : { version: x.version, state: "good" }));
  h.image.booted = next.version;
  h.image.pending_reboot = false;
}

// ---- pools: made, grown, shrunk and deleted, as the daemon answers ----

type Pool = Schema<"Pool">;

function poolOf(h: Host, uuid: string): Pool {
  const p = h.storage.pools.find((x) => x.uuid === uuid);
  if (!p) throw new DemoProblem(404, "unknown_pool", "There is no such pool.");
  if (p.operation && !p.operation.finished_at) throw new DemoProblem(409, "busy", "A disk is being removed from the pool.");
  return p;
}

/** Takes a spare disk out of the list, erased; a disk with contents needs the pool's name. */
function takeSpare(h: Host, path: string, name: string, confirm: unknown): Disk {
  const d = h.storage.unassigned.find((x) => x.path === path);
  if (!d) throw new DemoProblem(409, "disk_not_eligible", `${path} cannot go into a pool.`);
  if (d.contents && confirm !== name) throw new DemoProblem(422, "confirm", `${path} holds ${d.contents}; it is erased only with the pool's name.`);
  return d;
}

/** Labels the disks of a pool the way the daemon does, and sets its targets. */
function tier(p: Pool) {
  const count: Record<string, number> = {};
  const both = new Set(p.disks.map((d) => d.kind)).size === 2;
  for (const d of p.disks) {
    if (!d.bcachefs_label) {
      count[d.kind] = (count[d.kind] ?? p.disks.filter((o) => o.kind === d.kind && o.bcachefs_label).length) + 1;
      d.bcachefs_label = `${d.kind}.${d.kind}${count[d.kind]}`;
    }
    Object.assign(d, { fstype: "bcachefs", label: p.label, state: "rw", contents: "", has_children: false, data_target: both ? (d.kind === "ssd" ? "foreground" : "background") : "", metadata_target: both && d.kind === "ssd" ? "foreground" : "" });
  }
  const ssd = both ? "ssd" : "";
  Object.assign(p, { foreground_target: ssd, promote_target: ssd, metadata_target: ssd, background_target: both ? "hdd" : "" });
}

const rawSize = (p: Pool) => p.disks.reduce((n, d) => n + d.size, 0);

export function createPool(h: Host, body: Record<string, unknown>): Pool {
  const name = String(body.name ?? "");
  const paths = (body.disks as string[] | undefined) ?? [];
  const copies = Number(body.copies ?? 1);
  if (!/^[a-z0-9][a-z0-9-]{0,30}$/.test(name)) throw new DemoProblem(400, "invalid_name", "A pool's name has lowercase letters, digits and dashes.");
  if (h.storage.pools.some((p) => p.mount_name === name) || h.storage.partitions.some((p) => p.state === "mounted" && p.mount_name === name)) {
    throw new DemoProblem(409, "name_taken", "Another pool has that name.");
  }
  if (paths.length === 0 || copies > paths.length) throw new DemoProblem(409, "too_few_disks", `${copies} copies need ${copies} disks.`);
  const disks = paths.map((path) => takeSpare(h, path, name, body.confirm));
  h.storage.unassigned = h.storage.unassigned.filter((d) => !paths.includes(d.path));
  const p: Pool = {
    name, label: name, uuid: `0d5e7a10-demo-4000-8000-${String(h.now()).padStart(12, "0").slice(-12)}`, state: "mounted", mountdir: `/var/mnt/${name}`, mount_name: name,
    auto_mount: true, data_replicas: copies, metadata_replicas: copies, foreground_target: "", promote_target: "", background_target: "", metadata_target: "",
    disks, usage: { total_bytes: 0, used_bytes: 0, available_bytes: 0, used_percent: 0 },
  };
  tier(p);
  p.usage = { total_bytes: rawSize(p), used_bytes: 0, available_bytes: rawSize(p), used_percent: 0 };
  h.storage.pools.push(p);
  return p;
}

export function addPoolDisk(h: Host, uuid: string, body: Record<string, unknown>) {
  const p = poolOf(h, uuid);
  const d = takeSpare(h, String(body.disk ?? ""), p.name, body.confirm);
  h.storage.unassigned = h.storage.unassigned.filter((x) => x.path !== d.path);
  d.bcachefs_label = "";
  p.disks.push(d);
  tier(p);
  if (p.usage) {
    p.usage.total_bytes += d.size;
    p.usage.available_bytes += d.size;
    p.usage.used_percent = (p.usage.used_bytes / p.usage.total_bytes) * 100;
  }
}

/** A removal takes twelve seconds here: a quarter of the disk's data moves every three. */
export function removePoolDisk(h: Host, uuid: string, path: string): Pool {
  const p = poolOf(h, uuid);
  const d = p.disks.find((x) => x.path === path);
  if (!d) throw new DemoProblem(404, "unknown_disk", "The pool has no such disk.");
  const copies = Math.max(p.data_replicas, p.metadata_replicas, 1);
  if (p.disks.length - 1 < copies) throw new DemoProblem(409, "too_few_disks", `The pool keeps ${copies} copies and would have ${p.disks.length - 1} disks.`);
  if (!p.disks.some((o) => o !== d && o.kind === d.kind) && p.background_target) throw new DemoProblem(409, "last_of_role", `It is the pool's only ${d.kind} disk.`);
  const total = Math.round(((p.usage?.used_bytes ?? 0) * d.size) / Math.max(rawSize(p), 1));
  const op: Schema<"PoolOperation"> = { kind: "remove", disk: path, phase: "moving", started_at: iso(h.now()), total_bytes: total, left_bytes: total };
  p.operation = op;
  d.state = "evacuating";
  for (let i = 1; i <= 3; i++) {
    later(h, i * 3000, () => {
      if (p.operation === op) op.left_bytes = Math.round(total * (1 - i / 4));
    });
  }
  later(h, 12_000, () => {
    if (p.operation !== op) return;
    Object.assign(op, { phase: "done", left_bytes: 0, finished_at: iso(h.now()) });
    p.disks = p.disks.filter((x) => x !== d);
    if (p.usage) {
      p.usage.total_bytes -= d.size;
      p.usage.available_bytes = p.usage.total_bytes - p.usage.used_bytes;
      p.usage.used_percent = (p.usage.used_bytes / Math.max(p.usage.total_bytes, 1)) * 100;
    }
    h.storage.unassigned.push({ ...d, fstype: "", label: "", bcachefs_label: "", state: "", contents: "", data_target: "", metadata_target: "" });
  });
  return p;
}

/** Stops a removal, or clears one that ended. */
export function endRemoval(h: Host, uuid: string) {
  const p = h.storage.pools.find((x) => x.uuid === uuid);
  if (!p?.operation) throw new DemoProblem(404, "unknown_pool", "No removal runs on that pool.");
  const d = p.disks.find((x) => x.path === p.operation!.disk);
  if (d) d.state = "rw";
  p.operation = undefined;
}

export function deletePool(h: Host, uuid: string, confirm: string | null) {
  const p = poolOf(h, uuid);
  if (confirm !== p.name) throw new DemoProblem(422, "confirm", `Send the pool's name, ${p.name}; every disk in it is erased.`);
  const users = h.stacks.filter((s) => (s.variables ?? []).some((v) => v.kind === "path" && `${v.value}/`.startsWith(`${p.mountdir}/`)));
  if (users.length > 0) throw new DemoProblem(409, "pool_in_use", `The pool is in use by ${users.map((s) => `the stack ${s.name}`).join(", ")}.`);
  h.storage.pools = h.storage.pools.filter((x) => x !== p);
  h.storage.subvolumes = h.storage.subvolumes.filter((s) => !s.path.startsWith(`${p.mountdir}/`));
  h.storage.unassigned.push(...p.disks.map((d) => ({ ...d, fstype: "", label: "", bcachefs_label: "", state: "", contents: "", data_target: "", metadata_target: "" })));
}
