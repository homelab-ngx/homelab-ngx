import { describe, expect, it } from "vitest";
import { type CoreDeploy, deployOutcome, deployStep, stepsOf } from "./core.ts";

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
