/**
 * The installer page's view of a disk: its partitions and free space as
 * segments in disk order, with the system's own partitions grouped by what
 * they are (a system slot is its root and verity partitions; the empty slot
 * is the spare one image updates write into; then the Nix store and data).
 * The same grouping turns systemd-repart's plan into the disk after install.
 */
import type { Schema } from "#lib/api/client.ts";

export type Disk = Schema<"InstallDisk">;
export type Part = Schema<"InstallPartition">;
export type PlanRow = Schema<"InstallPlanRow">;
export type Holds = Part["holds"] | "free";

export interface Segment {
  key: string;
  holds: Holds;
  /** Partition names: "nvme0n1p3", or "nvme0n1p5–p6" for a group. */
  names: string;
  /** What the user reads: "Windows", "System 0.0.1", "Free". */
  title: string;
  fs: string;
  start: number;
  size: number;
  /** The partition, when the segment is exactly one. */
  part?: Part;
  /** Its partitions' names, one each: a system's root and its verity partition are two. None for free space. */
  members: string[];
  /** In a plan: what the install does to it. */
  change?: "new" | "kept" | "resized";
}

const ROOT = new Set(["4f68bce3-e8cd-4db1-96e7-fbcaf984b709", "root-x86-64"]);
const VERITY = new Set(["2c7357ed-ebd2-46d9-aec1-23d437ec2bf5", "root-x86-64-verity"]);
const VAR = new Set(["4d21b016-b534-45c2-a9fb-5c16e091fd2d", "var"]);
const NIX = "073d2b22-9f7e-4817-8d45-890277f7dd55";

export const HOLDS_WORD: Record<Holds, string> = {
  esp: "EFI",
  windows: "Windows",
  linux: "Linux",
  homelab: "Homelab",
  empty: "Empty",
  other: "Other",
  free: "Free",
};

/** The system group a partition belongs to, by its type and label. */
export function systemGroup(type: string, label: string): { id: string; title: string } | null {
  const t = type.toLowerCase();
  if (ROOT.has(t) || VERITY.has(t)) {
    if (label === "_empty") return { id: "spare", title: "System, spare" };
    if (label.startsWith("homelab_")) return { id: `system-${label}`, title: `System ${label.slice("homelab_".length)}` };
    return null;
  }
  if (t === NIX) return { id: "nix", title: "Nix store" };
  if (VAR.has(t) && label === "var") return { id: "data", title: "Data" };
  return null;
}

/** "nvme0n1p5", or "nvme0n1p5–p6" for a run of partitions. */
function names(list: string[]): string {
  if (list.length < 2) return list[0] ?? "";
  const last = list[list.length - 1] ?? "";
  const suffix = last.match(/p?\d+$/)?.[0] ?? last;
  return `${list[0]}–${suffix}`;
}

/** Folds the members of each system group into one segment, in disk order. */
function group(items: (Segment & { group?: { id: string; title: string } })[]): Segment[] {
  const out: Segment[] = [];
  const byId = new Map<string, { seg: Segment; members: string[] }>();
  for (const it of [...items].sort((a, b) => a.start - b.start)) {
    const { group: g, ...seg } = it;
    if (!g) {
      out.push(seg);
      continue;
    }
    const had = byId.get(g.id);
    if (had) {
      had.seg.size += seg.size;
      had.members.push(seg.names);
      had.seg.names = names(had.members);
      had.seg.members = [...had.seg.members, ...seg.members];
      had.seg.part = undefined;
      if (seg.change === "new" && had.seg.change !== "new") had.seg.change = seg.change;
      continue;
    }
    const merged: Segment = { ...seg, key: g.id, title: g.title, holds: "homelab" };
    byId.set(g.id, { seg: merged, members: [seg.names] });
    out.push(merged);
  }
  return out;
}

/** The disk now: its partitions and free runs. */
export function diskSegments(disk: Disk): Segment[] {
  const parts = disk.partitions.map((p) => ({
    key: p.name,
    holds: p.holds as Holds,
    names: p.name,
    title: p.holds === "empty" || p.holds === "other" ? p.label || HOLDS_WORD[p.holds] : p.label || HOLDS_WORD[p.holds],
    fs: p.fs,
    start: p.start,
    size: p.size,
    part: p,
    members: [p.name],
    group: systemGroup(p.type, p.label) ?? undefined,
  }));
  const free = disk.free.map((f) => ({ key: `free-${f.start}`, holds: "free" as const, names: "", title: "Free", fs: "", start: f.start, size: f.size, members: [] }));
  return group([...parts, ...free]);
}

/** The disk after the install, from repart's plan: what stays, what is new, and the free space left. */
export function planSegments(disk: Disk, rows: PlanRow[]): Segment[] {
  const byPath = new Map(disk.partitions.map((p) => [p.path, p]));
  const items = rows.map((r) => {
    const had = byPath.get(r.node);
    const isNew = r.activity === "create";
    const g = systemGroup(r.type, r.label);
    return {
      key: r.node,
      holds: (isNew ? (r.type === "esp" ? "esp" : "homelab") : (had?.holds ?? "other")) as Holds,
      names: r.node.replace(/^\/dev\//, ""),
      title: had && !isNew ? had.label || HOLDS_WORD[had.holds] : r.type === "esp" ? "EFI" : r.label || r.type,
      fs: had && !isNew ? had.fs : "",
      start: r.offset,
      size: r.size,
      change: (isNew ? "new" : r.activity === "resize" ? "resized" : "kept") as Segment["change"],
      members: [r.node.replace(/^\/dev\//, "")],
      group: isNew ? (g ?? undefined) : undefined,
    };
  });
  const sorted = [...items].sort((a, b) => a.start - b.start);
  const gaps: Segment[] = [];
  let at = 1 << 20;
  for (const s of sorted) {
    if (s.start - at >= 1 << 30) gaps.push({ key: `free-${at}`, holds: "free", names: "", title: "Free", fs: "", start: at, size: s.start - at, members: [] });
    at = Math.max(at, s.start + s.size);
  }
  if (disk.size - (1 << 20) - at >= 1 << 30) gaps.push({ key: `free-${at}`, holds: "free", names: "", title: "Free", fs: "", start: at, size: disk.size - (1 << 20) - at, members: [] });
  return group([...items, ...gaps]);
}

/** The largest free run, the segment the page selects first. */
export function largestFree(segs: Segment[]): Segment | undefined {
  return segs.filter((s) => s.holds === "free").sort((a, b) => b.size - a.size)[0];
}

/** The install's stages, in order, and where each starts on the progress bar. */
export const STAGES = [
  { label: "Partitioning", at: 2 },
  { label: "Copying the system", at: 15 },
  { label: "Formatting", at: 55 },
  { label: "Installing the boot loader", at: 80 },
  { label: "Setting name and network", at: 92 },
] as const;

export interface Progress {
  /** 1-based stage, STAGES.length + 1 when done. */
  stage: number;
  label: string;
  percent: number;
}

/**
 * Where an install is, from the step the daemon reports and the markers in
 * its log (systemd-repart prints no percentage: the bar moves by stage).
 */
export function installProgress(step: string, phase: string | undefined, log: readonly string[]): Progress {
  if (step === "installed") return { stage: STAGES.length + 1, label: "Installed", percent: 100 };
  let i = 0;
  if (phase === "finish") i = 4;
  else if (phase === "boot") i = 3;
  else if (log.some((l) => l.startsWith("Formatting future partition"))) i = 2;
  else if (log.some((l) => l.startsWith("Copying in "))) i = 1;
  const s = STAGES[i] ?? STAGES[0];
  return { stage: i + 1, label: s.label, percent: s.at };
}
