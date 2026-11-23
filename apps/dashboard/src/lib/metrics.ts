/**
 * The server overview's data: the daemon's metric frames (one column per
 * figure, aligned on `at`), how live samples join them, how they become
 * chart lines, and the plain-words mood of the host.
 */
import type { Schema } from "./api/client.ts";
import { bytes } from "./format.ts";

export type Frames = Schema<"MetricFrames">;
export type Series = Schema<"MetricSeries">;
export type HostInfo = Schema<"HostInfo">;
export type Snapshot = Schema<"MetricsSnapshot">;
export type MetricsSample = Schema<"MetricsSample">;
type Col = (number | null)[];

/** The per-scope figures every point carries. */
export type Figure =
  | "cpu"
  | "memory"
  | "pressure_cpu"
  | "pressure_memory"
  | "pressure_io"
  | "disk_read"
  | "disk_write"
  | "net_rx"
  | "net_tx";
const FIGURES: Figure[] = [
  "cpu",
  "memory",
  "pressure_cpu",
  "pressure_memory",
  "pressure_io",
  "disk_read",
  "disk_write",
  "net_rx",
  "net_tx",
];

/** How much the Live view keeps, in seconds. */
export const LIVE_SPAN = 15 * 60;

const nulls = (n: number): Col => Array.from({ length: n }, () => null);

function emptySeries(n: number): Series {
  return Object.fromEntries(FIGURES.map((f) => [f, nulls(n)])) as unknown as Series;
}

function joinSeries(a: Series | undefined, an: number, b: Series | undefined, bn: number, from: number): Series {
  const out = {} as Record<Figure, Col>;
  for (const f of FIGURES) {
    out[f] = [...(a?.[f] ?? nulls(an)), ...(b?.[f] ?? nulls(bn))].slice(from);
  }
  return out as unknown as Series;
}

/**
 * Appends b's points to a's, padding a stack that is missing on either side
 * with nulls, and drops points more than `keep` seconds older than the newest.
 */
export function appendFrames(a: Frames, b: Frames, keep = LIVE_SPAN): Frames {
  const at = [...a.at, ...b.at];
  const newest = at.at(-1) ?? 0;
  let from = 0;
  while (from < at.length && newest - at[from]! > keep) from++;
  const an = a.at.length;
  const bn = b.at.length;
  const stacks: Record<string, Series> = {};
  for (const name of new Set([...Object.keys(a.stacks), ...Object.keys(b.stacks)])) {
    stacks[name] = joinSeries(a.stacks[name], an, b.stacks[name], bn, from);
  }
  // A stack with no reading left in the window drops out.
  for (const [name, s] of Object.entries(stacks)) {
    if (s.cpu.every((v) => v === null)) delete stacks[name];
  }
  return {
    at: at.slice(from),
    step: b.step || a.step,
    host: joinSeries(a.host, an, b.host, bn, from),
    services: joinSeries(a.services, an, b.services, bn, from),
    sessions: joinSeries(a.sessions, an, b.sessions, bn, from),
    kernel: joinSeries(a.kernel, an, b.kernel, bn, from),
    stacks,
    temp_cpu: [...a.temp_cpu, ...b.temp_cpu].slice(from),
    temp_disk: [...a.temp_disk, ...b.temp_disk].slice(from),
    power_cpu: [...a.power_cpu, ...b.power_cpu].slice(from),
    power_wall: [...a.power_wall, ...b.power_wall].slice(from),
  };
}

export function emptyFrames(): Frames {
  return {
    at: [],
    step: 2,
    host: emptySeries(0),
    services: emptySeries(0),
    sessions: emptySeries(0),
    kernel: emptySeries(0),
    stacks: {},
    temp_cpu: [],
    temp_disk: [],
    power_cpu: [],
    power_wall: [],
  };
}

/** The last reading of a column, skipping gaps. */
export function latest(col: Col | undefined): number | undefined {
  if (!col) return undefined;
  for (let i = col.length - 1; i >= 0; i--) {
    const v = col[i];
    if (v !== null && v !== undefined) return v;
  }
  return undefined;
}

/** The average of the last n readings of a column, skipping gaps. */
export function recent(col: Col | undefined, n: number): number | undefined {
  const vals = (col ?? []).slice(-n).filter((v): v is number => v !== null);
  return vals.length ? vals.reduce((s, v) => s + v, 0) / vals.length : undefined;
}

/** A light and a dark step of one hue. */
export type Hue = { light: string; dark: string };

/**
 * Chart colours, validated as a set with the dataviz palette checks against
 * the dashboard's surfaces (#ffffff, #171b21), in this order: the parts of
 * core, then the stacks. Core's parts share a violet family after the core
 * host's purple: Services keeps the full violet, Claude a step lighter (light
 * mode) or darker (dark mode), and Kernel a violet grey, since it is what is
 * left over rather than something running.
 */
export const CORE_HUE: Hue = { light: "#6250d6", dark: "#9085e9" };
export const SESSIONS_HUE: Hue = { light: "#aa95e8", dark: "#524fb8" };
export const KERNEL_HUE: Hue = { light: "#77748b", dark: "#77748b" };
const ORANGE: Hue = { light: "#eb6834", dark: "#d95926" };
const BLUE: Hue = { light: "#2a78d6", dark: "#3987e5" };
const AQUA: Hue = { light: "#1baf7a", dark: "#199e70" };
export const STACK_HUES: Hue[] = [
  ORANGE,
  BLUE,
  AQUA,
  { light: "#eda100", dark: "#c98500" }, // yellow
  { light: "#e87ba4", dark: "#d55181" }, // magenta
  { light: "#008300", dark: "#0ca30c" }, // green
];
/** Stacks past the palette fold into one slate grey "Other". */
export const OTHER_HUE: Hue = { light: "#56606c", dark: "#a3adb9" };

/** Two or three lines of one scope (read and write, CPU and memory and disk). */
export const PAIR_HUES: Hue[] = [BLUE, ORANGE, AQUA];

/**
 * A stack's hue follows its name, not its rank in a chart: the installed
 * stacks sorted by name take the palette in order.
 */
export function stackHues(names: string[]): Map<string, Hue> {
  const sorted = [...names].sort();
  return new Map(sorted.map((n, i) => [n, STACK_HUES[i] ?? OTHER_HUE]));
}

/**
 * The parts of the server that are not stacks: label is how the charts name
 * one, short how it reads under Core in the table.
 */
export const PARTS: { key: "services" | "sessions" | "kernel"; label: string; short: string; hue: Hue }[] = [
  { key: "services", label: "Core (System Services)", short: "System Services", hue: CORE_HUE },
  { key: "sessions", label: "Core (User Sessions)", short: "User Sessions", hue: SESSIONS_HUE },
  { key: "kernel", label: "Core (Kernel)", short: "Kernel", hue: KERNEL_HUE },
];

/** A chart line; series is the scope it was drawn from, when it is one. */
export type Line = { label: string; hue: Hue; values: Col; href?: string; series?: Series };

/**
 * One line per scope for a figure that adds up (CPU, memory): each stack by
 * name, labelled with its title, then the parts of core. Stacks past the
 * palette are summed into "Other".
 */
export function byScope(f: Frames, fig: "cpu" | "memory", hues: Map<string, Hue>, title: (stack: string) => string = (n) => n): Line[] {
  const lines: Line[] = [];
  let other: Col | undefined;
  for (const name of Object.keys(f.stacks).sort()) {
    const hue = hues.get(name);
    const values = f.stacks[name]![fig];
    if (hue && hue !== OTHER_HUE) {
      lines.push({ label: title(name), hue, values, href: `/stacks/${name}`, series: f.stacks[name] });
    } else {
      other = (other ?? nulls(values.length)).map((v, i) =>
        v === null && values[i] === null ? null : (v ?? 0) + (values[i] ?? 0),
      );
    }
  }
  if (other) lines.push({ label: "Other", hue: OTHER_HUE, values: other });
  for (const p of PARTS) lines.push({ label: p.label, hue: p.hue, values: f[p.key][fig], series: f[p.key] });
  return lines;
}

export type Mood = "idle" | "light" | "medium" | "heavy";
/** What work waits for. */
export type Resource = "cpu" | "memory" | "disks";

/**
 * How the host is doing, from pressure over about the last minute: the
 * share of time some task had to wait for CPU, memory or disk. Nothing
 * waiting is idle under 10% CPU use and light load above it. CPU use of
 * 80% or more is medium load on its own, and then `waits` is unset.
 * `share` is the named resource's wait, or else the CPU use, in percent.
 */
export function mood(f: Frames): { mood: Mood; waits?: Resource; share: number } | undefined {
  const n = Math.max(1, Math.round(60 / (f.step || 2)));
  const cpu = recent(f.host.pressure_cpu, n);
  const mem = recent(f.host.pressure_memory, n);
  const io = recent(f.host.pressure_io, n);
  const use = recent(f.host.cpu, n) ?? 0;
  if (cpu === undefined || mem === undefined || io === undefined) return undefined;
  // Each resource against its medium-load line; the furthest over is named.
  const over: [Resource, number, number][] = [
    ["memory", mem / 2, mem],
    ["disks", io / 10, io],
    ["cpu", cpu / 10, cpu],
  ];
  const [worst, ratio, share] = over.reduce((a, b) => (b[1] > a[1] ? b : a));
  if (mem >= 10 || io >= 25 || cpu >= 40) return { mood: "heavy", waits: worst, share };
  if (ratio >= 1) return { mood: "medium", waits: worst, share };
  return { mood: use >= 80 ? "medium" : use >= 10 ? "light" : "idle", share: use };
}

/** A processor's name without the maker's marks, clock speed and graphics. */
export function cpuShort(name: string): string {
  return name
    .replace(/\((R|TM|tm)\)/g, "")
    .replace(/\s+@\s*[\d.]+\s*GHz/i, "")
    .replace(/\s+with\s+.*Graphics$/i, "")
    .replace(/\s+\d+-Core Processor$/i, "")
    .replace(/\s+CPU\b/, "")
    .replace(/\s+/g, " ")
    .trim();
}

/** How long since boot, in its largest unit: 6 d, 3 h, 12 min. */
export function uptime(bootedAt: number, now = Date.now()): string {
  const s = Math.max(0, now / 1000 - bootedAt);
  if (s >= 86400) return `${Math.floor(s / 86400)} d`;
  if (s >= 3600) return `${Math.floor(s / 3600)} h`;
  return `${Math.floor(s / 60)} min`;
}

/** The machine in one line: model · processor, threads · memory · uptime. */
export function machineLine(info: HostInfo, now = Date.now()): string {
  const cpu = info.cpu ? `${cpuShort(info.cpu)}, ${info.cores} threads` : `${info.cores} threads`;
  return [info.model, cpu, bytes(info.memory_total), info.booted_at ? `up ${uptime(info.booted_at, now)}` : ""].filter(Boolean).join(" · ");
}
