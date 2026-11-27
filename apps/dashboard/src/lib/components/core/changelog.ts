import { api, call, type Schema } from "#lib/api/client.ts";
import type { Tone } from "#lib/components/Pill.svelte";
import { compareVersions, pendingVersion, runningVersion, updateOf, type ImageStatus } from "./core.ts";

export type ReleaseChange = Schema<"ReleaseChange">;
export type ReleaseNote = Schema<"ReleaseNote">;
export type ReleaseChangelog = Schema<"ReleaseChangelog">;

export const changelogKey = ["daemon", "system-updates", "changelog"] as const;
export const fetchChangelog = () => call<ReleaseChangelog>(api.GET("/system-updates/changelog"));

export const SEEN_VERSION_KEY = "homelab.seen_version";

const AREA_LABELS: Record<string, string> = {
  dashboard: "Dashboard",
  stacks: "Stacks",
  backups: "Backups",
  storage: "Storage",
  daemon: "Daemon",
  host: "Host",
  image: "Image",
  updates: "Updates",
  notifications: "Notifications",
  drafts: "Drafts",
  installer: "Installer",
  other: "Other",
};

export const isReleaseVersion = (v: string): boolean => /^\d+\.\d+\.\d+$/.test(v);

export function areaLabel(area: string): string {
  const key = area.trim().toLowerCase();
  if (!key) return "Other";
  return AREA_LABELS[key] ?? `${key.charAt(0).toUpperCase()}${key.slice(1)}`;
}

/**
 * Releases covering an upgrade into `to` (newest first). When `from` is older
 * than `to`, includes every intermediate release in `(from, to]`.
 */
export function upgradeNotes(releases: readonly ReleaseNote[], to: string, from?: string): ReleaseNote[] {
  if (!to) return [];
  if (from && compareVersions(to, from) > 0) {
    const ranged = releases.filter((r) => compareVersions(r.version, from) > 0 && compareVersions(r.version, to) <= 0);
    if (ranged.length > 0) return ranged;
  }
  const exact = releases.find((r) => r.version === to);
  return exact ? [exact] : [];
}

/** Splits changelog releases into featured top entries and older folded ones. */
export function splitChangelog(
  releases: readonly ReleaseNote[],
  image: ImageStatus,
): { featured: ReleaseNote[]; earlier: ReleaseNote[] } {
  const running = runningVersion(image);
  if (running) {
    const newer = releases.filter((r) => compareVersions(r.version, running) > 0);
    if (newer.length > 0) {
      const featured = newer.slice(0, 3);
      const shown = new Set(featured.map((r) => r.version));
      return { featured, earlier: releases.filter((r) => !shown.has(r.version)) };
    }
  }
  return { featured: releases.slice(0, 1), earlier: releases.slice(1) };
}

/** Status badge shown beside a release entry on the Updates page. */
export function releaseBadge(version: string, image: ImageStatus): { tone: Tone; label: string } | null {
  if (version === pendingVersion(image)) return { tone: "info", label: "next boot" };
  if (version === updateOf(image)?.version) return { tone: "info", label: "ready" };
  if (image.release.latest && version === image.release.latest && compareVersions(version, runningVersion(image)) > 0) {
    return { tone: "info", label: "newest" };
  }
  if (version === runningVersion(image)) return { tone: "ok", label: "running" };
  return null;
}
