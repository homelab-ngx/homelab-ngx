/** Backups: the pure logic behind the Backups page and its forms. */
import type { Schema } from "./api/client.ts";
import type { Tone } from "./components/Pill.svelte";
import { bytes, duration, wallTime } from "./format.ts";

export type Backup = Schema<"Backup">;
export type Run = Schema<"BackupRun">;
export type RunDetail = Schema<"BackupRunDetail">;
export type RunItem = Schema<"BackupRunItem">;
export type Preview = Schema<"BackupPreview">;
export type PreviewItem = Schema<"BackupPreviewItem">;
export type Destination = Schema<"Destination">;
export type DestinationInput = Schema<"DestinationInput">;
export type Coverage = Schema<"Coverage">;
export type Exclude = Schema<"Exclude">;
export type Keep = Schema<"Keep">;
export type SourceInput = Schema<"SourceInput">;

export const backupKeys = {
  all: ["daemon", "backups"] as const,
  list: ["daemon", "backups", "list"] as const,
  coverage: ["daemon", "backups", "coverage"] as const,
  destinations: ["daemon", "backups", "destinations"] as const,
  runs: (id: number) => ["daemon", "backups", "runs", id] as const,
  run: (id: number) => ["daemon", "backups", "run", id] as const,
  logs: (id: number) => ["daemon", "backups", "logs", id] as const,
  preview: (id: number) => ["daemon", "backups", "preview", id] as const,
};

// ---- schedule ----

export type ScheduleMode = "daily" | "weekly" | "manual" | "custom";

/** A schedule as the picker shows it. cron is kept for a custom one. */
export interface Schedule {
  mode: ScheduleMode;
  /** HH:MM, 24-hour. */
  time: string;
  /** 0 is Sunday. */
  weekday: number;
  cron: string;
}

export const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"] as const;

const pad = (n: number) => String(n).padStart(2, "0");

/** Reads 5-field cron into the picker: `M H * * *` is daily, `M H * * D` weekly, empty by hand. */
export function parseSchedule(cron: string): Schedule {
  const base: Schedule = { mode: "manual", time: "04:00", weekday: 1, cron: cron.trim() };
  if (base.cron === "") return base;
  const m = /^(\d{1,2}) (\d{1,2}) \* \* (\*|[0-7])$/.exec(base.cron.replace(/\s+/g, " "));
  if (!m || Number(m[1]) > 59 || Number(m[2]) > 23) return { ...base, mode: "custom" };
  const time = `${pad(Number(m[2]))}:${pad(Number(m[1]))}`;
  if (m[3] === "*") return { ...base, mode: "daily", time };
  return { ...base, mode: "weekly", time, weekday: Number(m[3]) % 7 };
}

/** The cron a picker state stands for. */
export function toCron(s: Schedule): string {
  if (s.mode === "manual") return "";
  if (s.mode === "custom") return s.cron.trim();
  const [h, m] = s.time.split(":").map(Number);
  const at = `${m || 0} ${h || 0} * *`;
  return s.mode === "daily" ? `${at} *` : `${at} ${s.weekday}`;
}

/** A schedule in words, its time in the host's clock format. */
export function describeSchedule(cron: string): string {
  const s = parseSchedule(cron);
  if (s.mode === "manual") return "Manual";
  if (s.mode === "daily") return `Daily at ${wallTime(s.time)}`;
  if (s.mode === "weekly") return `${WEEKDAYS[s.weekday]}s at ${wallTime(s.time)}`;
  return s.cron;
}

// ---- restic keep ----

export type KeepPreset = "short" | "standard" | "long";

export const KEEP_PRESETS: Record<KeepPreset, { label: string; hint: string; keep: Keep }> = {
  short: { label: "Short", hint: "Daily for a week.", keep: { daily: 7, weekly: 0, monthly: 0, yearly: 0 } },
  standard: {
    label: "Standard",
    hint: "Daily for a week, weekly for a month, monthly for half a year.",
    keep: { daily: 7, weekly: 4, monthly: 6, yearly: 0 },
  },
  long: { label: "Long", hint: "Standard, plus yearly for three years.", keep: { daily: 7, weekly: 4, monthly: 6, yearly: 3 } },
};

/** The preset whose numbers these are, or "custom". */
export function presetOf(k: Keep): KeepPreset | "custom" {
  for (const name of Object.keys(KEEP_PRESETS) as KeepPreset[]) {
    const p = KEEP_PRESETS[name].keep;
    if (p.daily === k.daily && p.weekly === k.weekly && p.monthly === k.monthly && p.yearly === k.yearly) return name;
  }
  return "custom";
}

/** A keep rule in words. */
export function describeKeep(k: Keep): string {
  const parts = [
    k.daily ? `${k.daily} daily` : "",
    k.weekly ? `${k.weekly} weekly` : "",
    k.monthly ? `${k.monthly} monthly` : "",
    k.yearly ? `${k.yearly} yearly` : "",
  ].filter(Boolean);
  return parts.length ? `Keeps ${parts.join(", ")}` : "Keeps every snapshot";
}

// ---- state ----

export interface State {
  tone: Tone;
  label: string;
}

/** What a backup's row says first. */
export function backupState(b: Backup): State {
  if (b.running) return { tone: "info", label: "Running" };
  if (b.previewing) return { tone: "info", label: "Previewing" };
  if (!b.enabled) return { tone: "idle", label: "Off" };
  switch (b.last_run?.result) {
    case "failure":
      return { tone: "bad", label: "Failed" };
    case "partial":
      return { tone: "warn", label: "Partial" };
    case "cancelled":
      return { tone: "warn", label: "Stopped" };
  }
  if (b.overdue) return { tone: "warn", label: "Overdue" };
  if (b.last_run?.result === "success") return { tone: "ok", label: "OK" };
  if (!b.last_run) return { tone: "idle", label: "Never run" };
  return { tone: "idle", label: "Unknown" };
}

export const RUN_STATE: Record<Run["result"], State> = {
  running: { tone: "info", label: "Running" },
  success: { tone: "ok", label: "OK" },
  partial: { tone: "warn", label: "Partial" },
  failure: { tone: "bad", label: "Failed" },
  cancelled: { tone: "warn", label: "Stopped" },
  unknown: { tone: "idle", label: "Unknown" },
};

export const ITEM_STATE: Record<RunItem["result"], State> = {
  pending: { tone: "idle", label: "Waiting" },
  running: { tone: "info", label: "Copying" },
  success: { tone: "ok", label: "OK" },
  partial: { tone: "warn", label: "Partial" },
  failure: { tone: "bad", label: "Failed" },
  skipped: { tone: "idle", label: "Skipped" },
  cancelled: { tone: "warn", label: "Stopped" },
};

/** A run or a preview is in flight: the backup can be stopped, and nothing else started. */
export const busy = (b: Backup) => b.running || b.previewing;

/** A preview compares S3 and rclone destinations only. */
export const canPreview = (b: Backup) => b.destinations.some((d) => d.kind !== "restic");

// ---- the strip of days ----

/** How a day went: its worst run. "run" is a run in flight, "none" a day without one. */
export type DayTone = "ok" | "warn" | "bad" | "run" | "none";

export interface DayMark {
  /** The local day, YYYY-MM-DD. */
  key: string;
  tone: DayTone;
  /** The day's newest run, the one a click opens; null on a day without one. */
  run: Run | null;
  /** The day in words: "12 Nov: OK, 6 min 2 s, 212 MB". */
  label: string;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"] as const;
const dayKey = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;

const RESULT_TONE: Record<Run["result"], DayTone> = {
  success: "ok",
  partial: "warn",
  failure: "bad",
  cancelled: "warn",
  unknown: "bad",
  running: "run",
};
/** Worst first; a run in flight outranks a finished good one, never a bad one. */
const WORST: DayTone[] = ["bad", "warn", "run", "ok", "none"];
const DAY_WORD: Record<DayTone, string> = { ok: "OK", warn: "Partly failed", bad: "Failed", run: "Running", none: "Did not run" };

/**
 * One mark per calendar day, oldest first and today last. A day takes its
 * worst run's result and opens its newest run. Days are local days, counted
 * on the calendar and not in 24-hour steps, so a clock change never drops or
 * doubles one.
 */
export function dayMarks(runs: Run[], now = new Date(), days = 14): DayMark[] {
  const byDay = new Map<string, Run[]>();
  for (const r of runs) {
    const k = dayKey(new Date(r.started_at));
    byDay.set(k, [...(byDay.get(k) ?? []), r]);
  }
  const out: DayMark[] = [];
  for (let i = days - 1; i >= 0; i--) {
    const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() - i);
    const key = dayKey(d);
    const name = `${d.getDate()} ${MONTHS[d.getMonth()]}`;
    const list = [...(byDay.get(key) ?? [])].sort((a, b) => a.started_at.localeCompare(b.started_at) || a.id - b.id);
    const newest = list.at(-1) ?? null;
    if (!newest) {
      out.push({ key, tone: "none", run: null, label: `${name}: ${DAY_WORD.none.toLowerCase()}` });
      continue;
    }
    const tone = list.map((r) => RESULT_TONE[r.result]).sort((a, b) => WORST.indexOf(a) - WORST.indexOf(b))[0]!;
    // A stopped run marks the day like a partly failed one, and says what it was.
    const stopped = tone === "warn" && !list.some((r) => r.result === "partial");
    let label = `${name}: ${stopped ? "Stopped" : DAY_WORD[tone]}`;
    if (list.length > 1) label += `, ${list.length} runs`;
    else if (newest.result !== "running") label += `, ${duration(newest.started_at, newest.finished_at)}, ${bytes(newest.bytes)}`;
    out.push({ key, tone, run: newest, label });
  }
  return out;
}

export const TRIGGER: Record<Run["trigger"], string> = { cron: "Scheduled", manual: "Manual", imported: "Imported" };

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** What a backup covers, in a few words. */
export function covers(sources: { kind: SourceInput["kind"] }[]): string {
  const stack = sources.filter((s) => s.kind === "stack").length;
  const path = sources.filter((s) => s.kind === "path").length;
  const parts = [
    sources.some((s) => s.kind === "core") ? "Server settings" : "",
    stack ? plural(stack, "stack folder") : "",
    path ? plural(path, "other folder") : "",
  ].filter(Boolean);
  return parts.join(", ") || "Nothing yet";
}

/** Where a destination is, in one line. */
export function destinationWhere(d: Destination): string {
  if (d.s3) return `${[d.s3.bucket, d.s3.prefix].filter(Boolean).join("/")} at ${d.s3.endpoint.replace(/^https?:\/\//, "")}`;
  if (d.rclone) return `${d.rclone.remote}:${d.rclone.path}`;
  if (d.restic) return d.restic.repository;
  return "–";
}

export const KIND_LABEL: Record<Destination["kind"], string> = { s3: "S3 storage", rclone: "rclone remote", restic: "restic repository" };

/** How far a copy has come, 0 to 100; null while the tool is still counting. */
export function percent(done: number, total: number): number | null {
  if (total <= 0) return null;
  return Math.min(100, Math.round((done / total) * 100));
}

/** A run's or a preview's items under the destination they belong to. */
export function byDestination<T extends { kind: RunItem["kind"]; destination: string }>(items: T[]): { name: string; destination: T | null; folders: T[] }[] {
  const groups: { name: string; destination: T | null; folders: T[] }[] = [];
  for (const it of items) {
    let g = groups.find((x) => x.name === it.destination);
    if (!g) groups.push((g = { name: it.destination, destination: null, folders: [] }));
    if (it.kind === "destination") g.destination = it;
    else g.folders.push(it);
  }
  return groups;
}

// ---- the backup form's draft ----

export interface SourceDraft {
  kind: SourceInput["kind"];
  stack: string;
  folder: string;
  path: string;
  /** Folder at the destination. "" keeps the path the folder has on the host; "/" is the top of the destination. */
  remote: string;
  /** Compare by size only (S3 and rclone destinations). */
  sizeOnly: boolean;
  excludes: Exclude[];
}

/** A recommended folder, as the form ticks it. */
export interface FolderRef {
  stack: string;
  key: string;
}

const refKey = (stack: string, key: string) => `${stack}\u0000${key}`;

/** The recommended folders a backup can include: the ones that resolve to a folder on the server. */
export function selectableFolders(stacks: Coverage["stacks"]): FolderRef[] {
  return stacks.flatMap((s) => s.folders.filter((f) => !f.problem).map((f) => ({ stack: s.stack, key: f.key })));
}

/** Whether every one of these folders is ticked; false for none to tick. */
export function allTicked(sources: SourceDraft[], folders: FolderRef[]): boolean {
  const ticked = new Set(sources.filter((s) => s.kind === "stack").map((s) => refKey(s.stack, s.folder)));
  return folders.length > 0 && folders.every((f) => ticked.has(refKey(f.stack, f.key)));
}

/**
 * Ticks or unticks recommended folders. Sources that stay keep their object, and so their settings and
 * place in the list. An unticked folder's settings are parked, and come back when it is ticked again.
 */
export function tickFolders(sources: SourceDraft[], folders: FolderRef[], on: boolean, parked: Map<string, SourceDraft>): SourceDraft[] {
  const wanted = new Set(folders.map((f) => refKey(f.stack, f.key)));
  if (!on) {
    return sources.filter((s) => {
      const k = refKey(s.stack, s.folder);
      if (s.kind !== "stack" || !wanted.has(k)) return true;
      parked.set(k, s);
      return false;
    });
  }
  const have = new Set(sources.filter((s) => s.kind === "stack").map((s) => refKey(s.stack, s.folder)));
  const added = folders
    .filter((f) => !have.has(refKey(f.stack, f.key)))
    .map((f) => parked.get(refKey(f.stack, f.key)) ?? newSource("stack", { stack: f.stack, folder: f.key }));
  return [...sources, ...added];
}

export interface BackupDraft {
  name: string;
  enabled: boolean;
  schedule: Schedule;
  bwlimit: string;
  transfers: number;
  healthcheck: string;
  sources: SourceDraft[];
  destinations: number[];
}

export const sourceKey = (s: Pick<SourceDraft, "kind" | "stack" | "folder" | "path">) =>
  `${s.kind}\u0000${s.stack}\u0000${s.folder}\u0000${s.path}`;

export const newSource = (kind: SourceInput["kind"], over: Partial<SourceDraft> = {}): SourceDraft => ({
  kind,
  stack: "",
  folder: "",
  path: "",
  remote: "",
  sizeOnly: false,
  excludes: [],
  ...over,
});

/** The editable copy of a backup, or a blank one. */
export function draftOf(b: Backup | null): BackupDraft {
  if (!b) {
    return {
      name: "",
      enabled: true,
      schedule: { mode: "daily", time: "04:00", weekday: 1, cron: "" },
      bwlimit: "",
      transfers: 0,
      healthcheck: "",
      sources: [],
      destinations: [],
    };
  }
  return {
    name: b.name,
    enabled: b.enabled,
    schedule: parseSchedule(b.schedule),
    bwlimit: b.bwlimit,
    transfers: b.transfers,
    healthcheck: b.healthcheck_url,
    sources: b.sources.map((s) => ({
      kind: s.kind,
      stack: s.stack,
      folder: s.folder,
      path: s.path,
      remote: s.remote_path === null ? "" : s.remote_path || "/",
      sizeOnly: s.size_only,
      excludes: s.excludes.map((e) => ({ ...e })),
    })),
    destinations: b.destinations.map((d) => d.id),
  };
}

/** The request body a draft stands for. Blank skip patterns and blank folders are dropped. */
export function inputOf(d: BackupDraft): Schema<"BackupInput"> {
  return {
    name: d.name.trim(),
    enabled: d.enabled,
    schedule: toCron(d.schedule),
    bwlimit: d.bwlimit.trim(),
    transfers: Number(d.transfers) || 0,
    healthcheck_url: d.healthcheck.trim(),
    destination_ids: d.destinations,
    sources: d.sources
      .filter((s) => s.kind !== "path" || s.path.trim() !== "")
      .map((s) => {
        const excludes = s.excludes
          .map((e) => ({ pattern: e.pattern.trim(), reason: e.reason.trim() }))
          .filter((e) => e.pattern !== "");
        const out: SourceInput = { kind: s.kind, excludes, size_only: s.sizeOnly };
        if (s.kind === "stack") Object.assign(out, { stack: s.stack, folder: s.folder });
        if (s.kind === "path") out.path = s.path.trim();
        if (s.remote.trim()) out.remote_path = s.remote.trim().replace(/^\/+|\/+$/g, "");
        return out;
      }),
  };
}

/** Why a draft cannot be saved yet; "" when it can. */
export function draftProblem(d: BackupDraft): string {
  if (!d.name.trim()) return "Name is required.";
  if (d.schedule.mode === "custom" && d.schedule.cron.trim().split(/\s+/).length !== 5)
    return "A cron schedule has five parts, like 0 4 * * *.";
  const paths = d.sources.filter((s) => s.kind === "path" && s.path.trim());
  if (paths.some((s) => !s.path.trim().startsWith("/"))) return "Folder paths must start with /.";
  return "";
}

/** The recommended folders no backup includes, as "stack: label". */
export function uncovered(c: Coverage | undefined): { stack: string; key: string; label: string }[] {
  return (c?.stacks ?? []).flatMap((s) =>
    s.folders.filter((f) => f.covered_by.length === 0).map((f) => ({ stack: s.stack, key: f.key, label: f.label })),
  );
}

// ---- the destination form's draft ----

export interface DestinationDraft {
  name: string;
  kind: Destination["kind"];
  endpoint: string;
  region: string;
  bucket: string;
  prefix: string;
  accessKeyId: string;
  secretAccessKey: string;
  rcloneConfig: string;
  rclonePath: string;
  repository: string;
  existing: boolean;
  password: string;
  /** null leaves the stored credentials as they are. */
  env: { name: string; value: string }[] | null;
  deleteRemoved: boolean;
  keep: Keep;
}

export function destinationDraft(d: Destination | null): DestinationDraft {
  return {
    name: d?.name ?? "",
    kind: d?.kind ?? "s3",
    endpoint: d?.s3?.endpoint ?? "",
    region: d?.s3?.region ?? "",
    bucket: d?.s3?.bucket ?? "",
    prefix: d?.s3?.prefix ?? "",
    accessKeyId: d?.s3?.access_key_id ?? "",
    secretAccessKey: "",
    rcloneConfig: "",
    rclonePath: d?.rclone?.path ?? "",
    repository: d?.restic?.repository ?? "",
    existing: false,
    password: "",
    env: d ? null : [],
    deleteRemoved: d?.delete_removed ?? false,
    keep: d ? { ...d.keep } : { ...KEEP_PRESETS.standard.keep },
  };
}

/** The request body a destination draft stands for. Secrets left blank on an edit are left out, so the stored ones stay. */
export function destinationInput(d: DestinationDraft): DestinationInput {
  const out: DestinationInput = { name: d.name.trim(), kind: d.kind };
  if (d.kind === "s3") {
    out.delete_removed = d.deleteRemoved;
    out.s3 = {
      endpoint: d.endpoint.trim(),
      region: d.region.trim(),
      bucket: d.bucket.trim(),
      prefix: d.prefix.trim(),
      access_key_id: d.accessKeyId.trim(),
      ...(d.secretAccessKey ? { secret_access_key: d.secretAccessKey } : {}),
    };
  } else if (d.kind === "rclone") {
    out.delete_removed = d.deleteRemoved;
    out.rclone = { path: d.rclonePath.trim(), ...(d.rcloneConfig.trim() ? { config: d.rcloneConfig } : {}) };
  } else {
    out.keep = {
      daily: Number(d.keep.daily) || 0,
      weekly: Number(d.keep.weekly) || 0,
      monthly: Number(d.keep.monthly) || 0,
      yearly: Number(d.keep.yearly) || 0,
    };
    out.restic = { repository: d.repository.trim() };
    if (d.existing && d.password) out.restic.password = d.password;
    if (d.env) {
      out.restic.env = Object.fromEntries(d.env.filter((e) => e.name.trim()).map((e) => [e.name.trim(), e.value]));
    }
  }
  return out;
}

/** Why a destination draft cannot be saved yet; "" when it can. creating is false on an edit. */
export function destinationProblem(d: DestinationDraft, creating: boolean): string {
  if (!d.name.trim()) return "Name is required.";
  if (d.kind === "s3") {
    if (!d.endpoint.trim() || !d.bucket.trim() || !d.accessKeyId.trim()) return "Endpoint, bucket and access key id are required.";
    if (creating && !d.secretAccessKey) return "Secret access key is required.";
  } else if (d.kind === "rclone") {
    if (creating && !d.rcloneConfig.trim()) return "Remote config is required.";
  } else {
    if (!d.repository.trim()) return "Repository is required.";
    if (creating && d.existing && !d.password) return "Repository password is required.";
  }
  return "";
}
