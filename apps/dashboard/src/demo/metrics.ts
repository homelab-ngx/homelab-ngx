/**
 * The demo's metrics. Every figure is a function of the time alone, so the
 * live stream and the history agree and two visits see the same curves.
 */
import type { Schema } from "#lib/api/client.ts";
import type { Host } from "./host.ts";

type Frames = Schema<"MetricFrames">;
type Series = Schema<"MetricSeries">;

const GB = 1e9;
const MB = 1e6;

/** A number in [0, 1) from two integers: the same for the same input. */
function noise(a: number, b: number): number {
  let x = (a * 374761393 + b * 668265263) | 0;
  x = (x ^ (x >>> 13)) * 1274126177;
  return ((x ^ (x >>> 16)) >>> 0) / 4294967296;
}

/** A figure that drifts over the day, wobbles over minutes and jitters per point. */
function wave(t: number, key: number, base: number, swing: number): number {
  const day = Math.sin((t / 86400) * 2 * Math.PI + key);
  const minutes = Math.sin(t / 240 + key * 1.7);
  const v = base * (1 + swing * (0.5 * day + 0.3 * minutes + 0.4 * (noise(Math.floor(t / 2), key) - 0.5)));
  return Math.max(0, v);
}

const figures = ["cpu", "memory", "pressure_cpu", "pressure_memory", "pressure_io", "disk_read", "disk_write", "net_rx", "net_tx"] as const;

function emptySeries(): Series {
  return Object.fromEntries(figures.map((f) => [f, [] as (number | null)[]])) as unknown as Series;
}

function push(s: Series, t: number, key: number, cpu: number, memory: number, net: boolean, on: boolean) {
  const c = on ? wave(t, key, cpu, 0.9) : 0;
  s.cpu.push(on ? +c.toFixed(2) : null);
  s.memory.push(on ? Math.round(wave(t, key + 1, memory, 0.08)) : null);
  s.pressure_cpu.push(on ? +(c * 0.05).toFixed(2) : null);
  s.pressure_memory.push(on ? 0 : null);
  s.pressure_io.push(on ? +wave(t, key + 2, 0.4, 1.5).toFixed(2) : null);
  s.disk_read.push(on ? Math.round(wave(t, key + 3, cpu * 0.6 * MB, 1.8)) : null);
  s.disk_write.push(on ? Math.round(wave(t, key + 4, cpu * 0.3 * MB, 1.8)) : null);
  s.net_rx.push(on && net ? Math.round(wave(t, key + 5, cpu * 90_000, 1.6)) : null);
  s.net_tx.push(on && net ? Math.round(wave(t, key + 6, cpu * 240_000, 1.6)) : null);
}

export const info = (h: Host): Schema<"HostInfo"> => ({ cores: 8, memory_total: 32 * GB, model: "ASRock B550M Pro4", cpu: "AMD Ryzen 7 5700G with Radeon Graphics", booted_at: Math.floor(h.now() / 1000) - 6 * 86400 });

/** The points from `from` to `to` (unix seconds), one every `step`. */
export function frames(h: Host, from: number, to: number, step: number): Frames {
  const f: Frames = { at: [], step, host: emptySeries(), kernel: emptySeries(), services: emptySeries(), sessions: emptySeries(), stacks: {}, temp_cpu: [], temp_disk: [], power_cpu: [], power_wall: [] };
  for (const s of h.stacks) f.stacks[s.name] = emptySeries();
  for (let t = Math.ceil(from / step) * step; t <= to; t += step) {
    f.at.push(t);
    push(f.services, t, 101, 1.2, 620 * MB, false, true);
    push(f.sessions, t, 102, 0.3, 180 * MB, false, true);
    push(f.kernel, t, 103, 0.9, 1.4 * GB, false, true);
    h.stacks.forEach((s, i) => push(f.stacks[s.name]!, t, i * 10 + 1, s.usage?.cpu_percent ?? 1, s.usage?.memory_bytes ?? 200 * MB, true, s.running));
    const parts = [f.services, f.sessions, f.kernel, ...Object.values(f.stacks)];
    const n = f.at.length - 1;
    for (const k of figures) {
      const sum = parts.reduce((a, p) => a + (p[k][n] ?? 0), 0);
      f.host[k].push(k === "cpu" || k.startsWith("pressure") ? +sum.toFixed(2) : Math.round(sum));
    }
    f.temp_cpu.push(Math.round(41 + (f.host.cpu[n] ?? 0) * 0.6));
    f.temp_disk.push(Math.round(wave(t, 104, 37, 0.04)));
    f.power_cpu.push(+(4 + (f.host.cpu[n] ?? 0) * 0.5).toFixed(1));
    // The demo's plug sees the package plus about 18 W for the rest of the box.
    f.power_wall.push(+(22 + (f.host.cpu[n] ?? 0) * 0.6).toFixed(1));
  }
  return f;
}

const RANGES: Record<string, [span: number, step: number]> = { "24h": [86400, 300], "7d": [7 * 86400, 1800], "30d": [30 * 86400, 7200] };

export function history(h: Host, range: string): Schema<"MetricsHistory"> {
  const [span, step] = RANGES[range] ?? RANGES["24h"]!;
  const now = Math.floor(h.now() / 1000);
  // The demo tried power saver for a day, a day and a half ago.
  const changes = [
    { at: now - 36 * 3600, profile: "power-saver" },
    { at: now - 12 * 3600, profile: h.profile.profile },
  ].filter((c) => c.at >= now - span);
  return { frames: frames(h, now - span, now, step), profile_changes: changes };
}

/** What the live page listens to in place of the daemon's event stream. */
export interface MetricsSource {
  addEventListener(type: string, fn: (e: Event) => void): void;
  close(): void;
  onerror: ((e: Event) => void) | null;
}

const STEP = 2;

/** The last 15 minutes at once, then a point every two seconds. */
export function metricsSource(h: Host): MetricsSource {
  const on = new Map<string, (e: Event) => void>();
  const send = (type: string, data: unknown) => on.get(type)?.(new MessageEvent(type, { data: JSON.stringify(data) }));
  let last = Math.floor(h.now() / 1000 / STEP) * STEP;
  const first = setTimeout(() => send("snapshot", { info: info(h), frames: frames(h, last - 15 * 60, last, STEP) }), 0);
  const timer = setInterval(() => {
    const now = Math.floor(h.now() / 1000 / STEP) * STEP;
    if (now <= last) return;
    send("sample", { frames: frames(h, last + STEP, now, STEP) });
    last = now;
  }, STEP * 1000);
  return {
    addEventListener: (type, fn) => void on.set(type, fn),
    close: () => {
      clearTimeout(first);
      clearInterval(timer);
    },
    onerror: null,
  };
}
