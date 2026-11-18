/** Short display forms shared by the pages. */

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

export function stamp(iso: string | undefined | null): string {
  if (!iso) return "–";
  return new Date(iso).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
}

export function duration(from: string, to?: string | null, now = Date.now()): string {
  const s = Math.max(0, Math.round(((to ? new Date(to).getTime() : now) - new Date(from).getTime()) / 1000));
  // A finished thing that took no whole second still took some time.
  if (s === 0 && to) return "under 1 s";
  if (s < 60) return `${s} s`;
  return s % 60 === 0 ? `${s / 60} min` : `${Math.floor(s / 60)} min ${s % 60} s`;
}
