/** The Storage page's logic: usable space, disk roles and health, folder use. */
import type { Schema, Stack } from "./api/client.ts";
import type { Tone } from "./components/Pill.svelte";

type Pool = Schema<"Pool">;
type Disk = Schema<"Disk">;
type Subvolume = Schema<"Subvolume">;
type Entry = Schema<"Entry">;
type FolderFigures = Schema<"FolderFigures">;
type FolderUsage = Schema<"FolderUsage">;

export interface Capacity {
  used: number;
  total: number;
  free: number;
  percent: number;
}

/**
 * Space as the operator can use it. bcachefs reports raw space summed over
 * every disk, and each file is stored data_replicas times.
 */
export function capacity(p: Pool): Capacity | null {
  const u = p.usage;
  if (!u || u.total_bytes <= 0) return null;
  const copies = Math.max(1, p.data_replicas || 1);
  return {
    used: u.used_bytes / copies,
    total: u.total_bytes / copies,
    free: u.available_bytes / copies,
    percent: (u.used_bytes / u.total_bytes) * 100,
  };
}

/** Tone for how full a pool is. */
export const fullness = (percent: number): Tone => (percent > 90 ? "bad" : percent > 75 ? "warn" : "ok");

export type Role = "cache" | "storage" | "";

const inTarget = (label: string, target: string) => !!target && (label === target || label.startsWith(`${target}.`));

/**
 * A disk's job in a tiered pool: new writes and hot reads land on the cache
 * disks (foreground/promote target), and data settles on the storage disks
 * (background target). An untiered pool has no roles.
 */
export function role(p: Pool, d: Disk): Role {
  const label = d.bcachefs_label;
  if (!label || !p.background_target) return "";
  if (inTarget(label, p.background_target)) return "storage";
  if (inTarget(label, p.foreground_target) || inTarget(label, p.promote_target)) return "cache";
  return "";
}

export interface DiskState {
  tone: Tone;
  /** One lowercase word for the Health column; two where one would mislead ("bad sectors"). */
  word: string;
}

export function diskState(d: Disk): DiskState {
  const h = d.health;
  if (!h) return { tone: "idle", word: "unchecked" };
  if (!h.passed) return { tone: "bad", word: "failing" };
  if (h.bad_sectors > 0) return { tone: "warn", word: "bad sectors" };
  return { tone: "ok", word: "healthy" };
}

/** One line on the disks as a whole: the worst first, or "all healthy". */
export function disksSummary(disks: Disk[]): { tone: Tone; text: string } {
  const failing = disks.filter((d) => diskState(d).tone === "bad").length;
  const worn = disks.filter((d) => diskState(d).tone === "warn").length;
  const plural = (n: number) => `${n} disk${n === 1 ? "" : "s"}`;
  if (failing > 0) return { tone: "bad", text: `${plural(failing)} failing` };
  if (worn > 0) return { tone: "warn", text: `${plural(worn)} with bad sectors` };
  if (disks.length > 0 && disks.every((d) => d.health)) return { tone: "ok", text: "all healthy" };
  return { tone: "idle", text: "" };
}

/** The folders (subvolumes) on a mounted pool. */
export function foldersOf(p: Pool, subvolumes: Subvolume[]): Subvolume[] {
  if (p.state !== "mounted" || !p.mountdir) return [];
  return subvolumes.filter((s) => s.path.startsWith(`${p.mountdir}/`));
}

/** The stacks whose path variables point into a folder. */
export function usedBy(path: string, stacks: Stack[]): string[] {
  const inside = (v: string | undefined | null) => !!v && (v === path || v.startsWith(`${path}/`));
  return stacks
    .filter((s) => (s.variables ?? []).some((v) => v.kind === "path" && inside(v.value)))
    .map((s) => s.name)
    .sort();
}

export interface Segment {
  label: string;
  bytes: number;
  /** Share of the pool's usable size, 0–100. */
  percent: number;
  other?: boolean;
}

/**
 * The capacity bar split by folder: the biggest few by name, the rest as
 * "other". Folders not yet measured are left out. A folder's size counts
 * data its files share once, but data shared between two folders is in both,
 * and sizes are before compression: together they can exceed what the pool
 * reports as used, and the bar is then scaled down to the used share.
 */
export function segments(folders: Subvolume[], total: number, mountdir: string, used = total, named = 3): Segment[] {
  if (total <= 0) return [];
  const measured = folders.filter((f) => f.used_bytes > 0).sort((a, b) => b.used_bytes - a.used_bytes);
  const name = (path: string) => path.slice(mountdir.length + 1) || path;
  const seg = (label: string, bytes: number, other = false): Segment => ({
    label,
    bytes,
    percent: (bytes / total) * 100,
    ...(other ? { other } : {}),
  });
  const out = measured.slice(0, named).map((f) => seg(name(f.path), f.used_bytes));
  const rest = measured.slice(named).reduce((n, f) => n + f.used_bytes, 0);
  if (rest > 0) out.push(seg("other", rest, true));
  const cap = Math.min(100, (used / total) * 100);
  const sum = out.reduce((n, s) => n + s.percent, 0);
  if (sum > cap) for (const s of out) s.percent = (s.percent / sum) * cap;
  return out;
}

/** The oldest measurement among the folders, or null when none is measured. */
export function measuredAt(folders: Subvolume[]): string | null {
  const times = folders.map((f) => f.measured_at).filter((t): t is string => !!t);
  if (times.length === 0) return null;
  return times.reduce((min, t) => (t < min ? t : min));
}

/** The file browser's address for a host folder. */
export function browseHref(path: string): string {
  return `/storage/browse?path=${encodeURIComponent(path)}`;
}

export interface Crumb {
  label: string;
  path: string;
}

/** The folders from / down to path, one per breadcrumb. */
export function crumbs(path: string): Crumb[] {
  const out: Crumb[] = [{ label: "/", path: "/" }];
  for (const part of path.split("/").filter(Boolean)) {
    const parent = out[out.length - 1]!.path;
    out.push({ label: part, path: `${parent === "/" ? "" : parent}/${part}` });
  }
  return out;
}

export interface BrowseRow extends Entry {
  /** bcdu's figures, for a folder that has them. */
  usage?: FolderUsage;
  /** What the row weighs: a measured folder's total, a file's size. */
  bytes: number | null;
}

export type BrowseKey = "name" | "size" | "exclusive" | "shared" | "modified" | "owner";

export interface BrowseSort {
  key: BrowseKey;
  desc: boolean;
}

/** The order a folder opens in: the heaviest first. */
export const browseDefault: BrowseSort = { key: "size", desc: true };

/** The order a column starts in when it is first picked: sizes and dates from the top. */
export function browseSort(now: BrowseSort, key: BrowseKey): BrowseSort {
  if (now.key === key) return { key, desc: !now.desc };
  return { key, desc: key !== "name" && key !== "owner" };
}

const sortValue: Record<BrowseKey, (r: BrowseRow) => number | string | null> = {
  name: (r) => r.name,
  size: (r) => r.bytes,
  exclusive: (r) => r.usage?.exclusive ?? null,
  shared: (r) => r.usage?.shared ?? null,
  modified: (r) => r.modified_at,
  owner: (r) => `${r.owner}:${r.group}`,
};

/**
 * A folder's entries with bcdu's figures joined on. Folders always come
 * before files; within each, rows follow sort. A row with no value for the
 * sorted column goes last either way, and equal rows go by name.
 */
export function browseRows(entries: Entry[], figures?: FolderFigures | null, sort: BrowseSort = browseDefault): BrowseRow[] {
  const rows = entries.map((e): BrowseRow => {
    const usage = e.type === "dir" && figures?.available ? figures.folders[e.name] : undefined;
    return { ...e, ...(usage ? { usage } : {}), bytes: usage ? usage.total : e.type === "file" ? e.size : null };
  });
  const value = sortValue[sort.key];
  const folder = (r: BrowseRow) => (r.type === "dir" ? 0 : 1);
  return rows.sort((a, b) => {
    if (folder(a) !== folder(b)) return folder(a) - folder(b);
    const x = value(a);
    const y = value(b);
    if (x === null || y === null) return x === y ? a.name.localeCompare(b.name) : x === null ? 1 : -1;
    const order = typeof x === "string" ? x.localeCompare(y as string) : x - (y as number);
    return (sort.desc ? -order : order) || a.name.localeCompare(b.name);
  });
}
