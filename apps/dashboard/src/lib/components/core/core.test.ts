import { describe, expect, it } from "vitest";
import { type CoreDeploy, type ImageStatus, compareVersions, deployOutcome, deployStep, headline, runningVersion, stepsOf, updateOf } from "./core.ts";

const d = (over: Partial<CoreDeploy>): CoreDeploy => ({
  id: 1,
  kind: "deploy",
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
    expect(deployStep(d({ phase: "Build" }))).toBe(1);
    expect(deployStep(d({ phase: "Health check" }))).toBe(3);
  });
  it("marks every step done once healthy", () => {
    expect(deployStep(d({ phase: "Health check", result: "healthy" }))).toBe(4);
    expect(deployStep(d({ kind: "rollback", phase: "Health check", result: "healthy" }))).toBe(2);
  });
  it("stops on the health check when it went back", () => {
    expect(deployStep(d({ phase: "Rolling back" }))).toBe(3);
    expect(deployStep(d({ phase: "Rolling back", result: "rolled_back" }))).toBe(3);
  });
  it("stops on the build when that failed", () => {
    expect(deployStep(d({ phase: "Build", result: "build_failed" }))).toBe(1);
  });
});

describe("stepsOf and deployOutcome", () => {
  it("gives a rollback two steps", () => {
    expect(stepsOf(d({ kind: "rollback" }))).toEqual(["Switch", "Health check"]);
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
