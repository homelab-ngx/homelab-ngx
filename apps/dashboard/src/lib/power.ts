/**
 * Reboot and poweroff: the daemon stops the stacks, unmounts the pools,
 * then hands the host to systemd. The power page follows it through: an
 * update's install first when the reboot finishes one, the steps while the
 * daemon answers, then a countdown until it answers again from a new boot.
 */
import { api, call, type Schema } from "#lib/api/client.ts";
import type { Tone } from "#lib/components/Pill.svelte";
import { deployOutcome, deployStep, stepsOf, type CoreDeploy } from "#lib/components/core/core.ts";

export type PowerStatus = Schema<"PowerStatus">;
export type PowerStep = Schema<"PowerStep">;
export type PowerAction = "reboot" | "poweroff";

export const powerKey = ["daemon", "system", "power"] as const;
export const fetchPower = () => call<PowerStatus>(api.GET("/system/power"));

export const actionLabel: Record<PowerAction, string> = { reboot: "Reboot", poweroff: "Shut down" };

/** A shutdown is under way: the daemon is stopping the host. One that failed is not. */
export const underWay = (s: PowerStatus | undefined) => !!s?.action && s.phase !== "idle" && s.phase !== "failed";

export interface PowerExtra {
  /** The version the host should come back on. */
  to?: string;
  /** The reboot finishes an install this page follows first. */
  install?: boolean;
  /** When the page was asked for (ms); the timer counts from it. */
  t?: number;
}

/** Where the power page lives for a shutdown begun at boot `from`, coming back to `back`. */
export function powerHref(action: PowerAction, from: string, back: string, extra: PowerExtra = {}): string {
  const q = new URLSearchParams({ action, from, back });
  if (extra.to) q.set("to", extra.to);
  if (extra.install) q.set("install", "1");
  if (extra.t) q.set("t", String(extra.t));
  return `/power?${q}`;
}

/**
 * starting: asked, the daemon has not begun; refused: it said no;
 * installing: the update installs before the reboot; install_failed: it
 * did not, so there is no reboot; stopping: the daemon answers and stops
 * the host; failed: it could not; ready: the stacks are stopped and the
 * pools unmounted, and the host is handed to systemd; down: gone, the
 * countdown runs; late: past the countdown, still checking; back: answering
 * from a new boot; off: a poweroff went through.
 */
export type Stage = "starting" | "refused" | "installing" | "install_failed" | "stopping" | "failed" | "ready" | "down" | "late" | "back" | "off";

/** The host is gone, or back from a new boot: the old boot's steps are all it said. */
export const gone = (stage: Stage) => stage === "down" || stage === "late" || stage === "back" || stage === "off";

/** The stage ended the run: nothing more happens on this page. */
export const stuck = (stage: Stage) => stage === "refused" || stage === "install_failed" || stage === "failed";

export interface PowerView {
  stage: Stage;
  steps: PowerStep[];
  /** Seconds left of the countdown. */
  left: number;
  /** How much of the countdown has passed, 0 to 1. */
  done: number;
  /** How far the whole run is, 0 to 1: the overall bar. */
  share: number;
  error: string;
}

export interface PowerInput {
  action: PowerAction;
  /** The boot the shutdown began in. */
  from: string;
  /** The last answer, and whether the last read got one. */
  status?: PowerStatus;
  reachable: boolean;
  /** A read has finished; before that nothing is known. */
  checked?: boolean;
  /** When the host went down (ms); the countdown starts there. */
  downSince?: number;
  now: number;
  /** How long the last reboot took, in seconds. */
  expected: number;
  /** The request this page sent: not answered yet, or refused with this error. */
  start?: { pending: boolean; error: string };
  /**
   * Set when the reboot finishes an install: the install (once known) and
   * whether the page saw it end, or saw the shutdown begin after it.
   */
  install?: { deploy?: CoreDeploy; past: boolean };
}

/** Seconds the countdown adds to the last reboot, so a slower one still ends inside it. */
export const rebootMargin = 30;

/** Where each part of the run sits on the overall bar: [start, end]. */
function segments(action: PowerAction, install: boolean) {
  if (action === "poweroff") return { install: [0, 0], prep: [0, 0.85], reboot: [0.85, 0.95] } as const;
  return install ? ({ install: [0, 0.25], prep: [0.25, 0.45], reboot: [0.45, 0.97] } as const) : ({ install: [0, 0], prep: [0, 0.3], reboot: [0.3, 0.97] } as const);
}
const at = ([a, b]: readonly [number, number], f: number) => a + (b - a) * Math.min(1, Math.max(0, f));

/** How far the daemon is in stopping the host: the stacks are the first half, the mounts the second. */
function prepShare(s: PowerStatus | undefined): number {
  if (!s) return 0;
  const part = (kind: PowerStep["kind"]) => {
    const all = s.steps.filter((x) => x.kind === kind);
    if (all.length === 0) return 0.5;
    const n = all.reduce((sum, x) => sum + (x.state === "done" || x.state === "failed" ? 1 : x.state === "working" ? 0.4 : 0), 0);
    return n / all.length;
  };
  if (s.phase === "stopping_stacks") return part("stack") / 2;
  if (s.phase === "unmounting") return 0.5 + part("mount") / 2;
  if (s.phase === "going_down") return 1;
  return 0;
}

/** How far an install is: the steps done, and half of the one running. */
function installShare(d: CoreDeploy | undefined): number {
  if (!d) return 0;
  const n = stepsOf(d).length || 1;
  if (d.result === "healthy") return 1;
  return Math.min(1, (deployStep(d) + 0.5) / n);
}

export function powerView(i: PowerInput): PowerView {
  const steps = i.status?.steps ?? [];
  const total = i.expected + rebootMargin;
  const seg = segments(i.action, !!i.install);
  const base = { steps, left: total, done: 0, share: 0, error: "" };
  if (i.start?.error) return { ...base, stage: "refused", error: i.start.error };
  if (i.reachable && i.status && i.from && i.status.boot_id !== i.from) return { ...base, stage: "back", done: 1, left: 0, share: 1 };
  if (i.install && !i.install.past && i.install.deploy?.result !== "healthy") {
    const d = i.install.deploy;
    const share = at(seg.install, installShare(d));
    if (d && d.result !== "running" && d.result !== "healthy") return { ...base, steps: [], stage: "install_failed", share, error: deployOutcome(d) };
    if (d || i.reachable) return { ...base, steps: [], stage: d ? "installing" : "starting", share };
    if (!i.checked || i.start?.pending) return { ...base, steps: [], stage: "starting" };
    // Not reachable and the install unknown: the daemon restarts in it.
    return { ...base, steps: [], stage: "installing", share };
  }
  if (i.checked === false || (i.start?.pending && !i.status)) return { ...base, stage: "starting", share: at(seg.prep, 0) };
  if (i.reachable && i.status) {
    if (i.status.phase === "failed") return { ...base, stage: "failed", error: i.status.error ?? "", share: at(seg.prep, prepShare(i.status)) };
    if (i.status.phase === "going_down") return { ...base, stage: "ready", share: at(seg.prep, 1) };
    if (i.status.phase === "idle") return { ...base, stage: "starting", share: at(seg.prep, 0) };
    return { ...base, stage: "stopping", share: at(seg.prep, prepShare(i.status)) };
  }
  if (i.action === "poweroff") return { ...base, stage: "off", share: 1 };
  const gone = Math.max(0, (i.now - (i.downSince ?? i.now)) / 1000);
  const left = Math.max(0, Math.ceil(total - gone));
  const done = Math.min(1, gone / total);
  return { ...base, stage: left > 0 ? "down" : "late", left, done, share: at(seg.reboot, done) };
}

/** 83 → "1:23". */
export function clock(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/**
 * The big number: the time left while the host is away, the time past it
 * when late, otherwise the time since the page was asked for.
 */
export function timer(view: PowerView, i: { since: number; downSince?: number; now: number; expected: number }): { value: string; label: string } {
  if (view.stage === "down") return { value: clock(view.left), label: "left" };
  if (view.stage === "late") {
    const over = (i.now - (i.downSince ?? i.now)) / 1000 - (i.expected + rebootMargin);
    return { value: `+${clock(over)}`, label: "over" };
  }
  return { value: clock((i.now - i.since) / 1000), label: "elapsed" };
}

export type MilestoneState = "done" | "active" | "waiting" | "failed";
export interface Milestone {
  label: string;
  state: MilestoneState;
}

// How far the old boot got, by the last phase read from it.
const reached: Partial<Record<PowerStatus["phase"], number>> = { stopping_stacks: 0, unmounting: 1, going_down: 2, failed: 2 };

/**
 * The run's milestones for the stage track: the update installed (when the
 * reboot finishes one), the stacks stopped, the pools unmounted, then the
 * reboot and the host back (or the host off). `phase` is the last phase
 * read from the old boot; `sawReady` whether a read said it was ready. A
 * milestone the page did not see reached stays waiting.
 */
export function trackOf(action: PowerAction, stage: Stage, phase: PowerStatus["phase"] | undefined, sawReady: boolean, install?: { past: boolean }): Milestone[] {
  const out: Milestone[] = [];
  if (install) {
    const state: MilestoneState = install.past
      ? "done"
      : stage === "install_failed" || stage === "refused"
        ? "failed"
        : stage === "starting" || stage === "installing"
          ? "active"
          : "done";
    out.push({ label: "update", state });
    if (state !== "done") {
      out.push({ label: "stacks", state: "waiting" }, { label: "pools", state: "waiting" });
      if (action === "poweroff") out.push({ label: "off", state: "waiting" });
      else out.push({ label: "reboot", state: "waiting" }, { label: "back", state: "waiting" });
      return out;
    }
  }
  const at = sawReady ? 2 : (reached[phase ?? "idle"] ?? -1);
  const failedAt = stage === "failed" ? Math.min(at, 1) : -2;
  const step = (n: number): MilestoneState => {
    if (n === failedAt) return "failed";
    if (at > n) return "done";
    if (at === n && stage === "stopping") return "active";
    if (n === 0 && !install && stage === "refused") return "failed";
    if (n === 0 && (stage === "starting" || (stage === "stopping" && at < 0))) return "active";
    return "waiting";
  };
  out.push({ label: "stacks", state: step(0) }, { label: "pools", state: step(1) });
  if (action === "poweroff") {
    out.push({ label: "off", state: stage === "off" ? "done" : stage === "ready" ? "active" : "waiting" });
    return out;
  }
  const away = stage === "ready" || stage === "down" || stage === "late";
  out.push({ label: "reboot", state: stage === "back" ? "done" : away ? "active" : "waiting" });
  out.push({ label: "back", state: stage === "back" ? "done" : "waiting" });
  return out;
}

/** How a step's own bar looks: empty, running, full, or full in a warning or failure colour. */
export type Bar = "empty" | "run" | "full" | "warn" | "bad" | "unknown";

/** One row of the step list: a stack, a mount, or a step of the install. */
export interface Row {
  key: string;
  name: string;
  tone: Tone;
  word: string;
  bar: Bar;
  /** What it waits for, and when systemd kills it. */
  wait?: { what: string; when: string };
  detail?: string;
}

/**
 * A step's state as a status word and tone. Once the host is gone a step it
 * last reported unfinished is "not seen": it may have finished, but the page
 * cannot say.
 */
export function stepState(s: PowerStep, hostGone = false): { tone: Tone; word: string } {
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

const barOf: Record<Tone, Bar> = { ok: "full", info: "run", warn: "warn", bad: "bad", idle: "empty" };

/** The shutdown's steps as rows. */
export function powerRows(steps: PowerStep[], hostGone = false): Row[] {
  return steps.map((s) => {
    const st = stepState(s, hostGone);
    return {
      key: s.kind + s.name,
      name: s.name,
      ...st,
      bar: st.word === "not seen" ? "unknown" : barOf[st.tone],
      wait: holdup(s, hostGone),
      detail: s.detail || undefined,
    };
  });
}

/**
 * The install's steps as rows. `away`: the daemon does not answer, which
 * during the install is its restart.
 */
export function installRows(d: CoreDeploy | undefined, away: boolean): Row[] {
  const names = d ? stepsOf(d) : ["Update", "Restart", "Health check"];
  // A daemon that does not answer mid-install is in its restart, whatever
  // step the last answer named.
  const restart = names.indexOf("Restart");
  const current = Math.max(d ? deployStep(d) : 0, away && restart >= 0 ? restart : 0);
  const failed = !!d && d.result !== "running" && d.result !== "healthy";
  return names.map((name, n) => {
    const row = (tone: Tone, word: string, bar: Bar): Row => ({ key: `install-${name}`, name, tone, word, bar });
    if (n < current) return row("ok", "done", "full");
    if (n === current && failed) return row("bad", "failed", "bad");
    if (n === current) return away && n === restart ? row("info", "restarting", "run") : row("info", "running", "run");
    return row("idle", "waiting", "empty");
  });
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

/** One check the page made: answered, no answer, or the first answer from the new boot. */
export type Probe = "answer" | "miss" | "back";

/** The last checks the page made, newest last. */
export function probe(list: Probe[], p: Probe, keep = 36): Probe[] {
  const out = [...list, p];
  return out.length > keep ? out.slice(out.length - keep) : out;
}
