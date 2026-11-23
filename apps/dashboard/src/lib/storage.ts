/** The Storage page's logic: usable space, disk roles and health, folder use. */
import type { Schema, Stack } from "./api/client.ts";
import type { Tone } from "./components/Pill.svelte";
import type { TreeRow } from "./drafts.ts";

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
/** A pool folder's name on its pool: /var/mnt/pool/media is "media". The pool is named once, above its folders. */
export function folderName(path: string, mountdir: string): string {
  return (mountdir && path.startsWith(`${mountdir}/`) && path.slice(mountdir.length + 1)) || path;
}

export function segments(folders: Subvolume[], total: number, mountdir: string, used = total, named = 3): Segment[] {
  if (total <= 0) return [];
  const measured = folders.filter((f) => f.used_bytes > 0).sort((a, b) => b.used_bytes - a.used_bytes);
  const name = (path: string) => folderName(path, mountdir);
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

/**
 * The editor's address for a host file. root is the folder its file list
 * starts at, when that is not the file's own folder.
 */
export function editHref(path: string, root?: string): string {
  const q = new URLSearchParams({ path });
  if (root && root !== parentOf(path)) q.set("root", root);
  return `/storage/edit?${q}`;
}

/**
 * The file an editor address opens and the folder its file list starts at:
 * ?root= when the file is below it, else the file's own folder.
 */
export function editTarget(url: { searchParams: { get(name: string): string | null } }): { path: string; root: string } {
  const path = url.searchParams.get("path") ?? "";
  const r = url.searchParams.get("root");
  return { path, root: r && relativeTo(r, path) !== null ? r : parentOf(path) };
}

/** The folder a host path is in. */
export function parentOf(path: string): string {
  const i = path.replace(/\/+$/, "").lastIndexOf("/");
  return i <= 0 ? "/" : path.slice(0, i);
}

/** A host path inside dir; name may hold further folders. */
export function joinPath(dir: string, name: string): string {
  return `${dir === "/" ? "" : dir.replace(/\/+$/, "")}/${name}`;
}

/** path relative to dir, or null when it is not below dir. */
export function relativeTo(dir: string, path: string): string | null {
  const prefix = dir === "/" ? "/" : `${dir.replace(/\/+$/, "")}/`;
  return path.startsWith(prefix) && path.length > prefix.length ? path.slice(prefix.length) : null;
}

/** The folders that lead to a relative path, outermost first: "a/b/c" gives "a" and "a/b". */
export function ancestors(rel: string): string[] {
  const parts = rel.split("/").slice(0, -1);
  return parts.map((_, i) => parts.slice(0, i + 1).join("/"));
}

/**
 * The host editor's file list: the folders read so far, under the one it
 * starts at ("" in listings), folders first and each by name. An expanded
 * folder that has been read shows what is in it. A symlink is listed as a
 * file; a device or socket is left out.
 */
export function hostTree(listings: Record<string, Entry[]>, expanded: ReadonlySet<string>): TreeRow[] {
  const rows: TreeRow[] = [];
  const walk = (dir: string, depth: number) => {
    const entries = (listings[dir] ?? []).filter((e) => e.type !== "other");
    const byName = (a: Entry, b: Entry) => a.name.localeCompare(b.name);
    const prefix = dir ? `${dir}/` : "";
    for (const e of entries.filter((e) => e.type === "dir").sort(byName)) {
      const path = prefix + e.name;
      rows.push({ kind: "folder", path, name: e.name, depth });
      if (expanded.has(path)) walk(path, depth + 1);
    }
    for (const e of entries.filter((e) => e.type !== "dir").sort(byName)) rows.push({ kind: "file", path: prefix + e.name, name: e.name, depth });
  };
  walk("", 0);
  return rows;
}

/** Why a new file cannot be at rel in the host editor's folder; "" when it can. The daemon checks the rest. */
export function hostPathProblem(rel: string, rows: TreeRow[]): string {
  if (!rel) return "Give the file a name.";
  if (rel.startsWith("/")) return "Leave out the leading /.";
  const parts = rel.split("/");
  if (parts.some((p) => p === "" || p === "." || p === "..")) return "No empty parts, ./ or ../.";
  if (rows.some((r) => r.path === rel)) return `${rel} is already there.`;
  return "";
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

/** The host's own disk on the Storage page: how full it is, and the Nix store's share of what is used. */
export interface SystemDisk {
  /** The store has a partition of its own; the figures are that partition's. */
  own: boolean;
  total: number;
  used: number;
  free: number;
  percent: number;
  parts: { label: string; bytes: number; percent: number; color: string }[];
}

export function systemDisk(s: Schema<"NixStore">): SystemDisk | null {
  if (s.total_bytes <= 0) return null;
  const total = s.total_bytes;
  const used = Math.max(0, total - s.free_bytes);
  const of = (n: number) => (n / total) * 100;
  const store = s.used_bytes < 0 ? null : Math.min(s.used_bytes, used);
  const parts =
    s.own_partition || store === null
      ? [{ label: s.own_partition ? "Nix store" : "Used", bytes: used, percent: of(used), color: "var(--series-1)" }]
      : [
          { label: "Nix store", bytes: store, percent: of(store), color: "var(--series-1)" },
          { label: "Other", bytes: used - store, percent: of(used - store), color: "var(--muted)" },
        ];
  return { own: s.own_partition, total, used, free: s.free_bytes, percent: of(used), parts };
}

type PoolOperation = Schema<"PoolOperation">;

/** A spare disk's kind, as the page says it. */
export const kindWord = (d: Disk): string => (d.kind === "ssd" ? "SSD" : "HDD");

/** The most copies a pool of n disks can keep: one per disk, three at most. */
export const maxCopies = (n: number): number => Math.max(1, Math.min(3, n));

/** What the picked disks do together: SSDs cache for HDDs when both are there. */
export function tiering(disks: Disk[]): string {
  const ssd = disks.filter((d) => d.kind === "ssd").length;
  const hdd = disks.length - ssd;
  if (ssd === 0 || hdd === 0) return "";
  const n = (count: number, word: string) => `${count} ${word}${count === 1 ? "" : "s"}`;
  return `${n(ssd, "SSD")} ${ssd === 1 ? "caches" : "cache"} for ${n(hdd, "HDD")}`;
}

/** The picked disks that hold something: a pool erases them. */
export const withContents = (disks: Disk[]): Disk[] => disks.filter((d) => d.contents);

/** A removal that still runs. */
export const running = (op: PoolOperation | null | undefined): boolean => !!op && !op.finished_at;

/** Why a disk cannot leave its pool, as the daemon would answer; empty when it can. */
export function removeBlocked(p: Pool, d: Disk): string {
  const copies = Math.max(1, p.data_replicas, p.metadata_replicas);
  if (p.disks.length - 1 < copies) {
    return copies === 1 ? "It is the pool's only disk." : `The pool keeps ${copies} copies and needs ${copies} disks.`;
  }
  const r = role(p, d);
  if (r && !p.disks.some((o) => o.path !== d.path && role(p, o) === r)) return `It is the pool's only ${r} disk.`;
  return "";
}

export interface Removal {
  percent: number;
  /** One line: the disk and how far it is. */
  text: string;
}

/** How far the removal of a disk is. */
export function removal(op: PoolOperation, size: (n: number) => string): Removal {
  const disk = op.disk.replace("/dev/", "");
  const total = Math.max(op.total_bytes, 0);
  const moved = Math.min(total, Math.max(0, total - op.left_bytes));
  const percent = op.phase === "moving" ? (total > 0 ? Math.floor((moved / total) * 100) : 0) : 100;
  if (op.phase === "moving") return { percent, text: `Removing ${disk} · ${size(moved)} of ${size(total)} moved · ${percent}%` };
  if (op.phase === "failed") return { percent, text: `Removing ${disk} failed` };
  if (op.phase === "done") return { percent, text: `${disk} removed` };
  return { percent, text: `Removing ${disk} · ${op.phase}…` };
}

/** Where a typed path's suggestions come from: the folder it is in, and the start of the name being typed. */
export function pathParts(value: string): { parent: string; fragment: string } {
  const i = value.lastIndexOf("/");
  if (i < 0) return { parent: "/", fragment: value };
  return { parent: value.slice(0, i + 1), fragment: value.slice(i + 1) };
}

/** The folders in a listing whose names start with fragment, by name; a hidden one only when the fragment starts with ".". */
export function folderSuggestions(entries: Entry[], fragment: string, limit = 50): string[] {
  const f = fragment.toLowerCase();
  return entries
    .filter((e) => e.type === "dir" && e.name.toLowerCase().startsWith(f) && (f.startsWith(".") || !e.name.startsWith(".")))
    .map((e) => e.name)
    .sort((a, b) => a.localeCompare(b))
    .slice(0, limit);
}

/** A path without its trailing slash, which the daemon refuses; "/" stays. */
export const trimSlash = (p: string): string => (p.length > 1 ? p.replace(/\/+$/, "") || "/" : p);
