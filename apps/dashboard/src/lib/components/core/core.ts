/** Readings for the Core host page: system generations. */
import type { Schema } from "#lib/api/client.ts";

export type UpdateEvent = Schema<"UpdateEvent">;

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

/** One line for a generation on the timeline. */
export function eventTitle(ev: UpdateEvent): string {
  if (ev.baseline) return "First recorded generation";
  if (ev.source === "rollback") return `Rolled back · ${plural(ev.total_changes, "change")}`;
  if (ev.total_changes === 0) return ev.dependencies > 0 ? "Dependencies only" : "Configuration only";
  return plural(ev.total_changes, "change");
}

export const KIND_MARK: Record<string, string> = {
  upgrade: "↑",
  downgrade: "↓",
  added: "+",
  removed: "−",
  changed: "~",
};

export type CoreDeploy = Schema<"CoreDeploy">;

/** The steps a core deploy reports as its phase. A rollback has no fetch or build. */
export const deploySteps = ["Fetch", "Build", "Switch", "Health check"] as const;
export const rollbackSteps = ["Switch", "Health check"] as const;

/** An image host's operations (#97). */
export const imageSteps: Record<string, readonly string[]> = {
  build: ["Fetch", "Build", "Stage"],
  "core-update": ["Update", "Restart", "Health check"],
  "image-update": ["Update"],
};

export const stepsOf = (d: CoreDeploy): readonly string[] => imageSteps[d.kind] ?? (d.kind === "rollback" ? rollbackSteps : deploySteps);

/** What an operation is, for its card's title. */
export function deployTitle(d: CoreDeploy, short: (rev: string) => string): string {
  switch (d.kind) {
    case "rollback":
      return "Going back one version";
    case "build":
      return `Building from main${d.rev ? ` at ${short(d.rev)}` : ""}`;
    case "core-update":
      return `Updating core${d.version ? ` to ${d.version}` : ""}`;
    case "image-update":
      return `Installing the image${d.version ? ` ${d.version}` : ""}`;
  }
  return `Updating core${d.rev ? ` to ${short(d.rev)}` : ""}`;
}

/** The step a deploy is on, or stopped on. All of them are done once it is healthy. */
export function deployStep(d: CoreDeploy): number {
  const steps = stepsOf(d);
  if (d.result === "healthy") return steps.length;
  // Going back to the generation before happens because the health check failed.
  if (d.phase === "Rolling back" || d.result === "rolled_back") return steps.indexOf("Health check");
  return Math.max(0, steps.indexOf(d.phase));
}

/** One line for how a deploy ended. */
export function deployOutcome(d: CoreDeploy): string {
  switch (d.result) {
    case "running":
      return d.phase === "Rolling back" ? "Going back to the version before" : d.phase;
    case "healthy":
      if (d.kind in imageSteps) return d.phase;
      return d.kind === "rollback" ? "Back on the version before" : "Up to date";
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
