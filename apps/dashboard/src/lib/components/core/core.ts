/** Readings for the Updates page: versions and updates. */
import type { Schema } from "#lib/api/client.ts";

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
