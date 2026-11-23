import { describe, expect, it } from "vitest";
import {
  type CoreDeploy,
  type ImageStatus,
  compareVersions,
  deployOutcome,
  deployRows,
  deployShare,
  deployStep,
  headline,
  runningVersion,
  stepsOf,
  typicalSeconds,
  updateOf,
} from "./core.ts";

const d = (over: Partial<CoreDeploy>): CoreDeploy => ({
  id: 1,
  kind: "core-update",
  rev: "",
  phase: "Starting",
  result: "running",
  reason: "",
  failed_units: [],
  started_at: "2026-11-16T12:00:00Z",
  finished_at: null,
  log: "",
  ...over,
});

describe("deployStep", () => {
  it("follows the phase while running", () => {
    expect(deployStep(d({ phase: "Starting" }))).toBe(0);
    expect(deployStep(d({ phase: "Restart" }))).toBe(1);
    expect(deployStep(d({ phase: "Health check" }))).toBe(2);
  });
  it("marks every step done once healthy", () => {
    expect(deployStep(d({ phase: "Health check", result: "healthy" }))).toBe(3);
    expect(deployStep(d({ kind: "image-update", phase: "Update", result: "healthy" }))).toBe(1);
  });
  it("stops on the health check when it went back", () => {
    expect(deployStep(d({ phase: "Roll back" }))).toBe(2);
    expect(deployStep(d({ phase: "Roll back", result: "rolled_back" }))).toBe(2);
  });
  it("stops on the build when that failed", () => {
    expect(deployStep(d({ kind: "build", phase: "Build", result: "build_failed" }))).toBe(1);
  });
});

describe("stepsOf and deployOutcome", () => {
  it("gives an image update one step", () => {
    expect(stepsOf(d({ kind: "image-update" }))).toEqual(["Update"]);
  });
  it("says nothing changed when the build failed", () => {
    expect(deployOutcome(d({ result: "build_failed" }))).toMatch(/Nothing on the homelab changed/);
  });
});

const img = (over: Partial<ImageStatus> = {}): ImageStatus => ({
  booted: "0.2.2",
  installed: [
    { version: "0.2.2", state: "booted" },
    { version: "0.1.0", state: "good" },
  ],
  available: [],
  pending_reboot: false,
  core: { active: "0.2.2", installed: ["0.2.2"], available: [] },
  source: "",
  release: { url: "", latest: "0.2.2", checked_at: null },
  ...over,
});

describe("versions on an image host", () => {
  it("orders versions as the daemon does", () => {
    const sorted = ["0.1.1", "0.1.0.202611190130", "0.0.202611181531", "0.1.0"].sort(compareVersions);
    expect(sorted).toEqual(["0.0.202611181531", "0.1.0", "0.1.0.202611190130", "0.1.1"]);
  });
  it("shows the newer of the image and the core as the running version", () => {
    expect(runningVersion(img({ booted: "0.0.202611182048", core: { active: "0.0.202611182129", installed: [], available: [] } }))).toBe("0.0.202611182129");
    expect(runningVersion(img({ core: { active: "", installed: [], available: [] } }))).toBe("0.2.2");
  });
  it("takes the newest download as the update; a new image needs a reboot", () => {
    expect(updateOf(img())).toBeNull();
    expect(updateOf(img({ available: ["0.2.3", "0.2.1"] }))).toEqual({ version: "0.2.3", reboot: true });
    expect(updateOf(img({ core: { active: "0.2.2", installed: [], available: ["0.2.3"] } }))).toEqual({ version: "0.2.3", reboot: false });
  });
  it("says first what waits: a reboot, an update, or nothing", () => {
    expect(headline(img())).toEqual({ tone: "ok", state: "up to date", detail: "" });
    expect(headline(img({ available: ["0.2.3"] }))).toEqual({ tone: "info", state: "update ready", detail: "0.2.3 · needs a reboot" });
    expect(headline(img({ core: { active: "0.2.2", installed: [], available: ["0.2.3"] } })).detail).toBe("0.2.3 · no reboot");
    const waiting = img({ pending_reboot: true, available: ["0.2.4"], installed: [{ version: "0.2.3", state: "pending" }, { version: "0.2.2", state: "booted" }] });
    expect(headline(waiting)).toEqual({ tone: "info", state: "reboot to finish", detail: "0.2.3" });
  });
});

describe("an operation's bars", () => {
  const start = "2026-11-23T10:00:00Z";
  const t0 = Date.parse(start);
  const build = (over: Partial<CoreDeploy> = {}) => d({ id: 9, kind: "build", phase: "Build", result: "running", started_at: start, finished_at: null, ...over });

  it("gives each step a row with its own bar", () => {
    expect(deployRows(build()).map((r) => `${r.name}:${r.bar}`)).toEqual(["Fetch:full", "Build:run", "Stage:empty"]);
    expect(deployRows(build({ result: "build_failed" })).map((r) => r.word)).toEqual(["done", "failed", "waiting"]);
  });

  it("moves with the clock inside its step, using the last build's time", () => {
    const last = build({ id: 8, result: "healthy", started_at: "2026-11-22T10:00:00Z", finished_at: "2026-11-22T10:10:00Z" });
    expect(typicalSeconds(build(), [build(), last])).toBe(600);
    // Five minutes into a ten-minute build, on its second step of three.
    expect(deployShare(build(), t0 + 300_000, 600)).toBeCloseTo(0.5);
    // Past the build's time, it stays inside the step it is on.
    expect(deployShare(build(), t0 + 3_600_000, 600)).toBeCloseTo(2 / 3 - 0.02);
    expect(deployShare(build(), t0, 600)).toBeCloseTo(1 / 3 + 0.02);
    expect(deployShare(build(), t0)).toBeCloseTo(0.5);
    expect(deployShare(build({ result: "healthy" }), t0)).toBe(1);
  });
});
