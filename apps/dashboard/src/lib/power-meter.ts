/** The power meter and the profile comparison: the pure logic behind them. */
import { api, call, type Schema } from "./api/client.ts";
import type { Tone } from "./components/Pill.svelte";
import { PROFILES, profileLabel, watts, type PowerProfile } from "./power-profile.ts";

export type PowerMeter = Schema<"PowerMeter">;
export type PowerMeterTest = Schema<"PowerMeterTest">;
export type PowerProfiles = Schema<"MetricsPowerProfiles">;
export type ProfilePower = Schema<"MetricsProfilePower">;
export type ProfileChange = Schema<"MetricsProfileChange">;
export type CompareRange = "7d" | "30d";

export const powerMeterKey = ["daemon", "system", "power-meter"] as const;
export const fetchPowerMeter = () => call<PowerMeter>(api.GET("/system/power-meter"));

export const powerProfilesKey = (range: CompareRange, idleOnly: boolean) => ["daemon", "metrics", "power-profiles", range, idleOnly] as const;
export const fetchPowerProfiles = (range: CompareRange, idleOnly: boolean) =>
  call<PowerProfiles>(api.GET("/metrics/power-profiles", { params: { query: { range, idle_only: idleOnly } } }));

/** The connection's state as one mark and word. */
export function meterState(m: PowerMeter): { tone: Tone; word: string } {
  switch (m.state) {
    case "connected":
      return { tone: "ok", word: "connected" };
    case "connecting":
      return { tone: "info", word: "connecting" };
    case "auth_failed":
      return { tone: "bad", word: "refused" };
    case "error":
      return { tone: "bad", word: "error" };
  }
  return { tone: "idle", word: "off" };
}

/** A stretch of minutes, in the largest unit that keeps it readable. */
export function span(minutes: number): string {
  if (minutes < 60) return `${minutes} min`;
  if (minutes < 48 * 60) return `${Math.round(minutes / 60)} h`;
  return `${Math.round(minutes / 1440)} d`;
}

const known = (p: string): p is PowerProfile => PROFILES.some((x) => x.value === p);

/** A profile's name for a row or a chart mark. */
export const profileName = (p: string) => (known(p) ? profileLabel(p) : p);

/**
 * The comparison's rows in the profiles' order, each with its difference
 * from Balanced at the wall (or at the CPU package without a meter).
 */
export function compareRows(rows: ProfilePower[]): { name: string; time: string; wall: string; cpu: string; delta: string }[] {
  const order = (p: string) => {
    const i = PROFILES.findIndex((x) => x.value === p);
    return i < 0 ? PROFILES.length : i;
  };
  const sorted = [...rows].sort((a, b) => order(a.profile) - order(b.profile));
  const base = rows.find((r) => r.profile === "balanced");
  const pick = (r: ProfilePower) => (base?.wall != null ? r.wall : r.cpu);
  return sorted.map((r) => {
    const v = pick(r);
    const b = base ? pick(base) : null;
    const d = r.profile === "balanced" || v == null || b == null ? null : v - b;
    return {
      name: profileName(r.profile),
      time: span(r.minutes),
      wall: watts(r.wall) || "–",
      cpu: watts(r.cpu) || "–",
      delta: d === null ? "" : `${d > 0 ? "+" : "−"}${watts(Math.abs(d))}`,
    };
  });
}

/** The profile changes as chart marks. */
export const profileMarks = (changes: ProfileChange[] | undefined) => (changes ?? []).map((c) => ({ at: c.at, label: profileName(c.profile) }));
