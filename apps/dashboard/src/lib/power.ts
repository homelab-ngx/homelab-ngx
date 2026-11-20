/**
 * Reboot and poweroff: the daemon stops the stacks, unmounts the pools,
 * then hands the host to systemd. The power page follows it through: the
 * steps while the daemon answers, then a countdown until it answers again
 * from a new boot.
 */
import { api, call, type Schema } from "#lib/api/client.ts";

export type PowerStatus = Schema<"PowerStatus">;
export type PowerStep = Schema<"PowerStep">;
export type PowerAction = "reboot" | "poweroff";

export const powerKey = ["daemon", "system", "power"] as const;
export const fetchPower = () => call<PowerStatus>(api.GET("/system/power"));

export const actionLabel: Record<PowerAction, string> = { reboot: "Reboot", poweroff: "Shut down" };

/** A shutdown is under way: the daemon is stopping the host. One that failed is not. */
export const underWay = (s: PowerStatus | undefined) => !!s?.action && s.phase !== "idle" && s.phase !== "failed";

/** Where the power page lives for a shutdown begun at boot `from`, coming back to `back`. */
export function powerHref(action: PowerAction, from: string, back: string): string {
  const q = new URLSearchParams({ action, from, back });
  return `/power?${q}`;
}

/**
 * stopping: the daemon answers and stops the host; failed: it could not;
 * down: gone, the countdown runs; late: past the countdown, still checking;
 * back: answering from a new boot; off: a poweroff went through.
 */
export type Stage = "stopping" | "failed" | "down" | "late" | "back" | "off";

export interface PowerView {
  stage: Stage;
  steps: PowerStep[];
  /** Seconds left of the countdown. */
  left: number;
  /** How much of the countdown has passed, 0 to 1. */
  done: number;
  error: string;
}

export interface PowerInput {
  action: PowerAction;
  /** The boot the shutdown began in. */
  from: string;
  /** The last answer, and whether the last read got one. */
  status?: PowerStatus;
  reachable: boolean;
  /** When the host went down (ms); the countdown starts there. */
  downSince?: number;
  now: number;
  /** The countdown, in seconds. */
  expected: number;
}

export function powerView(i: PowerInput): PowerView {
  const steps = i.status?.steps ?? [];
  const base = { steps, left: i.expected, done: 0, error: "" };
  if (i.reachable && i.status) {
    if (i.from && i.status.boot_id !== i.from) return { ...base, stage: "back", done: 1, left: 0 };
    if (i.status.phase === "failed") return { ...base, stage: "failed", error: i.status.error ?? "" };
    return { ...base, stage: "stopping" };
  }
  if (i.action === "poweroff") return { ...base, stage: "off" };
  const gone = Math.max(0, (i.now - (i.downSince ?? i.now)) / 1000);
  const left = Math.max(0, Math.ceil(i.expected - gone));
  const done = i.expected > 0 ? Math.min(1, gone / i.expected) : 1;
  return { ...base, stage: left > 0 ? "down" : "late", left, done };
}

/** 83 → "1:23". */
export function clock(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** A step's state as a status word and tone. */
export function stepState(s: PowerStep): { tone: "ok" | "warn" | "bad" | "info" | "idle"; word: string } {
  const mount = s.kind === "mount";
  switch (s.state) {
    case "done":
      return { tone: "ok", word: mount ? "unmounted" : "stopped" };
    case "working":
      return { tone: "info", word: mount ? "unmounting" : "stopping" };
    case "failed":
      return mount ? { tone: "warn", word: "busy" } : { tone: "bad", word: "killed" };
  }
  return { tone: "idle", word: "waiting" };
}
