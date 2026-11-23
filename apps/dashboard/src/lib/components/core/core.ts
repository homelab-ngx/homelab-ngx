/** Readings for the Updates page: versions and updates. */
import type { Schema } from "#lib/api/client.ts";
import type { Tone } from "#lib/components/Pill.svelte";
import type { Bar, Row } from "#lib/steps.ts";

export type CoreDeploy = Schema<"CoreDeploy">;

/** The steps each kind of update reports as its phase. */
export const imageSteps: Record<string, readonly string[]> = {
  build: ["Fetch", "Build", "Stage"],
  download: ["Check", "Verify", "Download", "Stage"],
  "core-update": ["Update", "Restart", "Health check"],
  "image-update": ["Update"],
  install: ["Update", "Restart", "Health check"],
};

export const stepsOf = (d: CoreDeploy): readonly string[] => imageSteps[d.kind] ?? [];

/** What an operation is, for its card's title. */
export function deployTitle(d: CoreDeploy, short: (rev: string) => string): string {
  switch (d.kind) {
    case "build":
      return `Building from main${d.rev ? ` at ${short(d.rev)}` : ""}`;
    case "download":
      return d.version ? `Downloading ${d.version}` : "Checking for updates";
    case "core-update":
    case "image-update":
    case "install":
      return `Updating${d.version ? ` to ${d.version}` : ""}`;
  }
  return "Updating";
}

/** The step an update is on, or stopped on. All of them are done once it is healthy. */
export function deployStep(d: CoreDeploy): number {
  const steps = stepsOf(d);
  if (d.result === "healthy") return steps.length;
  // Going back to the version before happens because the health check failed.
  if (d.phase === "Roll back" || d.result === "rolled_back") return steps.indexOf("Health check");
  return Math.max(0, steps.indexOf(d.phase));
}

/**
 * An operation's steps as rows with a bar each. `away`: the daemon does not
 * answer, which while an operation runs is its restart.
 */
export function deployRows(d: CoreDeploy | undefined, away = false): Row[] {
  const names = d ? stepsOf(d) : imageSteps.install!;
  // A daemon that does not answer mid-install is in its restart, whatever
  // step the last answer named.
  const restart = names.indexOf("Restart");
  const current = Math.max(d ? deployStep(d) : 0, away && restart >= 0 ? restart : 0);
  const failed = !!d && d.result !== "running" && d.result !== "healthy";
  return names.map((name, n) => {
    const row = (tone: Tone, word: string, bar: Bar): Row => ({ key: `step-${name}`, name, tone, word, bar });
    if (n < current) return row("ok", "done", "full");
    if (n === current && failed) return row("bad", "failed", "bad");
    if (n === current) return away && n === restart ? row("info", "restarting", "run") : row("info", "running", "run");
    return row("idle", "waiting", "empty");
  });
}

/** How long the last operation of this kind took that ended well, in seconds; undefined when none did. */
export function typicalSeconds(d: CoreDeploy, all: CoreDeploy[]): number | undefined {
  const prev = all.find((x) => x.id !== d.id && x.kind === d.kind && x.result === "healthy" && x.finished_at);
  if (!prev?.finished_at) return undefined;
  const s = (Date.parse(prev.finished_at) - Date.parse(prev.started_at)) / 1000;
  return s > 0 ? s : undefined;
}

/**
 * How far an operation is, 0 to 1. Each step has an equal share. With the
 * time the last one of its kind took, the bar moves with the clock inside
 * the step it is on, and never past it.
 */
export function deployShare(d: CoreDeploy, now: number, typical?: number): number {
  const n = stepsOf(d).length || 1;
  if (d.result === "healthy") return 1;
  const step = Math.min(deployStep(d), n - 1);
  const lo = step / n;
  const hi = (step + 1) / n;
  if (d.result !== "running") return lo;
  if (!typical) return (step + 0.5) / n;
  const t = (now - Date.parse(d.started_at)) / 1000 / typical;
  return Math.min(hi - 0.02, Math.max(lo + 0.02, t));
}

/** One line for how an update ended. */
export function deployOutcome(d: CoreDeploy): string {
  switch (d.result) {
    case "running":
      return d.phase === "Roll back" ? "Going back to the version before" : d.phase;
    case "healthy":
      return d.phase;
    case "rolled_back":
      return "Didn't come up, so the homelab went back to the version before";
    case "build_failed":
      return "Couldn't be built. Nothing on the homelab changed";
    case "interrupted":
      return "Stopped before it finished";
    default:
      return "Failed";
  }
}

export type ImageStatus = Schema<"ImageStatus">;

/** Orders two versions the way the daemon does: dot-separated numbers. */
export function compareVersions(a: string, b: string): number {
  const x = a.split(".").map(Number);
  const y = b.split(".").map(Number);
  for (let i = 0; i < Math.max(x.length, y.length); i++) {
    const d = (x[i] ?? 0) - (y[i] ?? 0);
    if (d !== 0) return d;
  }
  return 0;
}

/** The one version the page shows: the newer of the image and the core. */
export function runningVersion(image: ImageStatus): string {
  const core = image.core.active;
  if (!core || !image.booted) return core || image.booted;
  return compareVersions(core, image.booted) > 0 ? core : image.booted;
}

/** The downloaded version an update would install; a new image needs a reboot. */
export function updateOf(image: ImageStatus): { version: string; reboot: boolean } | null {
  if (image.available[0]) return { version: image.available[0], reboot: true };
  if (image.core.available[0]) return { version: image.core.available[0], reboot: false };
  return null;
}

/** The version that is installed and starts at the next reboot. */
export const pendingVersion = (image: ImageStatus): string =>
  image.pending_reboot ? (image.installed.find((v) => v.state === "pending")?.version ?? "") : "";

export interface Headline {
  tone: "ok" | "info";
  /** The state, in the status mark's words. */
  state: string;
  /** The version it is about and what taking it means. */
  detail: string;
}

/** What the page says first: waiting for a reboot, an update, or nothing to do. */
export function headline(image: ImageStatus): Headline {
  const pending = pendingVersion(image);
  if (image.pending_reboot) return { tone: "info", state: "reboot to finish", detail: pending };
  const update = updateOf(image);
  if (update) return { tone: "info", state: "update ready", detail: `${update.version} · ${update.reboot ? "needs a reboot" : "no reboot"}` };
  return { tone: "ok", state: "up to date", detail: "" };
}

/** An installed version's state, in the page's words. */
export const VERSION_STATE: Record<string, { tone: "ok" | "idle" | "info" | "bad"; label: string }> = {
  booted: { tone: "ok", label: "running" },
  good: { tone: "idle", label: "fallback" },
  pending: { tone: "info", label: "next boot" },
  bad: { tone: "bad", label: "failed" },
};
