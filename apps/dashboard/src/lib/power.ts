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
 * ready: the stacks are stopped and the pools unmounted, and the host is
 * handed to systemd; down: gone, the countdown runs; late: past the
 * countdown, still checking; back: answering from a new boot; off: a
 * poweroff went through.
 */
export type Stage = "stopping" | "failed" | "ready" | "down" | "late" | "back" | "off";

/** The host is gone, or back from a new boot: the old boot's steps are all it said. */
export const gone = (stage: Stage) => stage === "down" || stage === "late" || stage === "back" || stage === "off";

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
  /** How long the last reboot took, in seconds. */
  expected: number;
}

/** Seconds the countdown adds to the last reboot, so a slower one still ends inside it. */
export const rebootMargin = 30;

export function powerView(i: PowerInput): PowerView {
  const steps = i.status?.steps ?? [];
  const total = i.expected + rebootMargin;
  const base = { steps, left: total, done: 0, error: "" };
  if (i.reachable && i.status) {
    if (i.from && i.status.boot_id !== i.from) return { ...base, stage: "back", done: 1, left: 0 };
    if (i.status.phase === "failed") return { ...base, stage: "failed", error: i.status.error ?? "" };
    if (i.status.phase === "going_down") return { ...base, stage: "ready" };
    return { ...base, stage: "stopping" };
  }
  if (i.action === "poweroff") return { ...base, stage: "off" };
  const gone = Math.max(0, (i.now - (i.downSince ?? i.now)) / 1000);
  const left = Math.max(0, Math.ceil(total - gone));
  const done = Math.min(1, gone / total);
  return { ...base, stage: left > 0 ? "down" : "late", left, done };
}

/** 83 → "1:23". */
export function clock(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

export type MilestoneState = "done" | "active" | "waiting";
export interface Milestone {
  label: string;
  state: MilestoneState;
}

// How far the old boot got, by the last phase read from it.
const reached: Partial<Record<PowerStatus["phase"], number>> = { stopping_stacks: 0, unmounting: 1, going_down: 2, failed: 2 };

/**
 * The shutdown's milestones: the stacks stopped, the pools unmounted, then
 * the reboot and the host back (or the host off). `phase` is the last phase
 * read from the old boot; `sawReady` whether a read said it was ready. A
 * milestone the page did not see reached stays waiting.
 */
export function milestones(action: PowerAction, stage: Stage, phase: PowerStatus["phase"] | undefined, sawReady: boolean): Milestone[] {
  const at = sawReady ? 2 : (reached[phase ?? "idle"] ?? -1);
  const step = (n: number): MilestoneState => (at > n ? "done" : at === n && stage === "stopping" ? "active" : "waiting");
  const out: Milestone[] = [
    { label: "stacks stopped", state: step(0) },
    { label: "pools unmounted", state: step(1) },
  ];
  if (action === "poweroff") {
    out.push({ label: "off", state: stage === "off" ? "done" : stage === "ready" ? "active" : "waiting" });
    return out;
  }
  const away = stage === "ready" || stage === "down" || stage === "late";
  out.push({ label: "rebooting", state: stage === "back" ? "done" : away ? "active" : "waiting" });
  out.push({ label: "back", state: stage === "back" ? "done" : "waiting" });
  return out;
}

/**
 * A step's state as a status word and tone. Once the host is gone a step it
 * last reported unfinished is "not seen": it may have finished, but the page
 * cannot say.
 */
export function stepState(s: PowerStep, hostGone = false): { tone: "ok" | "warn" | "bad" | "info" | "idle"; word: string } {
  const mount = s.kind === "mount";
  if (hostGone && (s.state === "waiting" || s.state === "working")) return { tone: "idle", word: "not seen" };
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

/**
 * What a stack still stopping after 30 s waits for, and when systemd kills
 * it. Nothing for a step that is not stopping, or once the host is gone.
 */
export function holdup(s: PowerStep, hostGone = false): { what: string; when: string } | undefined {
  const w = s.waiting_on;
  if (!w || hostGone || s.state !== "working") return undefined;
  const what = w.description && w.description !== w.unit ? `${w.description} (${w.unit})` : w.unit;
  return { what: `Waiting for ${what}`, when: w.kill_in > 0 ? `killed in ${clock(w.kill_in)}` : "killing" };
}
