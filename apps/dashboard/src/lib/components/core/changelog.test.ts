import { describe, expect, it } from "vitest";
import { areaLabel, isReleaseVersion, releaseBadge, splitChangelog, upgradeNotes, type ReleaseNote } from "./changelog.ts";
import type { ImageStatus } from "./core.ts";

const img = (over: Partial<ImageStatus> = {}): ImageStatus => ({
  booted: "0.4.5",
  installed: [
    { version: "0.4.5", state: "booted" },
    { version: "0.4.4", state: "good" },
  ],
  available: [],
  pending_reboot: false,
  core: { active: "0.4.5", installed: ["0.4.5"], available: [] },
  source: "",
  release: { url: "", latest: "0.4.5", checked_at: null },
  ...over,
});

const note = (version: string, text = `change in ${version}`): ReleaseNote => ({
  version,
  date: "2026-11-26",
  changes: [{ area: "dashboard", text }],
});

describe("changelog helpers", () => {
  it("detects release versions and area labels", () => {
    expect(isReleaseVersion("0.4.6")).toBe(true);
    expect(isReleaseVersion("0.4.6.202611270900")).toBe(false);
    expect(isReleaseVersion("dev")).toBe(false);
    expect(areaLabel("host")).toBe("Host");
    expect(areaLabel("custom")).toBe("Custom");
    expect(areaLabel("")).toBe("Other");
  });

  it("collects intermediate releases between from and to", () => {
    const releases = [note("0.4.7"), note("0.4.6"), note("0.4.5"), note("0.4.4")];
    expect(upgradeNotes(releases, "0.4.6", "0.4.4").map((r) => r.version)).toEqual(["0.4.6", "0.4.5"]);
    expect(upgradeNotes(releases, "0.4.6").map((r) => r.version)).toEqual(["0.4.6"]);
    expect(upgradeNotes(releases, "0.4.8", "0.4.6").map((r) => r.version)).toEqual(["0.4.7"]);
    expect(upgradeNotes(releases, "0.4.8", "0.4.7")).toEqual([]);
  });

  it("features releases newer than running, or the newest one when up to date", () => {
    const releases = [note("0.4.6"), note("0.4.5"), note("0.4.4")];
    const upToDate = splitChangelog(releases, img());
    expect(upToDate.featured.map((r) => r.version)).toEqual(["0.4.6"]);
    expect(upToDate.earlier.map((r) => r.version)).toEqual(["0.4.5", "0.4.4"]);

    const withRunningTop = splitChangelog([note("0.4.5"), note("0.4.4")], img());
    expect(withRunningTop.featured.map((r) => r.version)).toEqual(["0.4.5"]);
    expect(withRunningTop.earlier.map((r) => r.version)).toEqual(["0.4.4"]);
  });

  it("assigns badges for next boot, ready, newest, and running", () => {
    expect(releaseBadge("0.4.5", img())).toEqual({ tone: "ok", label: "running" });
    expect(releaseBadge("0.4.4", img())).toBeNull();
    expect(releaseBadge("0.4.6", img({ available: ["0.4.6"], release: { url: "", latest: "0.4.6", checked_at: null } }))).toEqual({
      tone: "info",
      label: "ready",
    });
    expect(releaseBadge("0.4.6", img({ release: { url: "", latest: "0.4.6", checked_at: null } }))).toEqual({
      tone: "info",
      label: "newest",
    });
    expect(
      releaseBadge(
        "0.4.6",
        img({
          pending_reboot: true,
          installed: [
            { version: "0.4.6", state: "pending" },
            { version: "0.4.5", state: "booted" },
          ],
        }),
      ),
    ).toEqual({ tone: "info", label: "next boot" });
  });
});
