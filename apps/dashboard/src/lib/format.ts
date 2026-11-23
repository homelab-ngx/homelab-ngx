/** Short display forms shared by the pages. */
import { clock } from "./clock.svelte.ts";

export function shortRev(rev: string | undefined): string {
  return rev ? rev.slice(0, 7) : "–";
}

export function bytes(n: number | undefined): string {
  if (n === undefined) return "–";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

/** Percent of one core, top-style. */
export function cpu(pct: number | undefined): string {
  if (pct === undefined) return "–";
  return pct < 1 && pct > 0 ? "<1%" : `${Math.round(pct)}%`;
}

const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

export function ago(iso: string | undefined | null, now = Date.now()): string {
  if (!iso) return "–";
  const s = Math.round((new Date(iso).getTime() - now) / 1000);
  const abs = Math.abs(s);
  if (abs < 60) return rtf.format(s, "second");
  if (abs < 3600) return rtf.format(Math.round(s / 60), "minute");
  if (abs < 86400) return rtf.format(Math.round(s / 3600), "hour");
  return rtf.format(Math.round(s / 86400), "day");
}

/** The host's clock format as Intl options; none follows the browser. */
function cycle(): Intl.DateTimeFormatOptions {
  if (clock.format === "12h") return { hourCycle: "h12" };
  if (clock.format === "24h") return { hourCycle: "h23" };
  return {};
}

/** A date and time in the host's clock format: 3 Mar, 14:05. */
export function dateTime(at: Date | string | number, opts: Intl.DateTimeFormatOptions = { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }): string {
  return new Date(at).toLocaleString(undefined, { ...opts, ...cycle() });
}

/** A time of day in the host's clock format: 14:05. */
export function clockTime(at: Date | string | number, seconds = false): string {
  return new Date(at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: seconds ? "2-digit" : undefined, ...cycle() });
}

/** A wall-clock "HH:MM" (a schedule's, in the host's time) in the host's clock format. */
export function wallTime(hhmm: string): string {
  const [h, m] = hhmm.split(":").map(Number);
  return clockTime(new Date(2000, 0, 1, h || 0, m || 0));
}

export function stamp(iso: string | undefined | null): string {
  if (!iso) return "–";
  return dateTime(iso);
}

export function duration(from: string, to?: string | null, now = Date.now()): string {
  const s = Math.max(0, Math.round(((to ? new Date(to).getTime() : now) - new Date(from).getTime()) / 1000));
  // A finished thing that took no whole second still took some time.
  if (s === 0 && to) return "under 1 s";
  if (s < 60) return `${s} s`;
  return s % 60 === 0 ? `${s / 60} min` : `${Math.floor(s / 60)} min ${s % 60} s`;
}
