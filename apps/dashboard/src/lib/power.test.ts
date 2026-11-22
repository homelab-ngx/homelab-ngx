import { describe, expect, it } from "vitest";
import { clock, powerHref, powerView, stepState, underWay, type PowerInput, type PowerStatus } from "./power.ts";

const status = (p: Partial<PowerStatus> = {}): PowerStatus => ({
  phase: "stopping_stacks",
  action: "reboot",
  steps: [{ kind: "stack", name: "media", state: "working" }],
  busy: [],
  frozen: [],
  boot_id: "boot-1",
  expected_seconds: 90,
  ...p,
});

const input = (p: Partial<PowerInput> = {}): PowerInput => ({
  action: "reboot",
  from: "boot-1",
  status: status(),
  reachable: true,
  now: 100_000,
  expected: 90,
  ...p,
});

describe("powerView", () => {
  it("shows the steps while the daemon stops the host", () => {
    const v = powerView(input());
    expect([v.stage, v.steps.length, v.left]).toEqual(["stopping", 1, 90]);
  });

  it("counts down from when the host went away", () => {
    const v = powerView(input({ reachable: false, downSince: 70_000 }));
    expect([v.stage, v.left, v.done]).toEqual(["down", 60, 1 / 3]);
    // The steps it last showed stay on the page.
    expect(v.steps).toHaveLength(1);
  });

  it("keeps checking past the countdown", () => {
    const v = powerView(input({ reachable: false, downSince: 0 }));
    expect([v.stage, v.left, v.done]).toEqual(["late", 0, 1]);
  });

  it("is back when the daemon answers from another boot", () => {
    expect(powerView(input({ status: status({ boot_id: "boot-2", phase: "idle" }) })).stage).toBe("back");
  });

  it("says why a shutdown failed", () => {
    const v = powerView(input({ status: status({ phase: "failed", error: "the reboot did not start" }) }));
    expect([v.stage, v.error]).toEqual(["failed", "the reboot did not start"]);
  });

  it("calls a poweroff done when the daemon is gone", () => {
    expect(powerView(input({ action: "poweroff", reachable: false })).stage).toBe("off");
  });

  it("starts the countdown on a page opened during the reboot", () => {
    const v = powerView(input({ status: undefined, reachable: false, downSince: undefined }));
    expect([v.stage, v.left]).toEqual(["down", 90]);
  });
});

describe("helpers", () => {
  it("sends pages to the power page only while a shutdown runs", () => {
    expect([underWay(status()), underWay(status({ phase: "failed" })), underWay(status({ phase: "idle", action: undefined }))]).toEqual([true, false, false]);
  });

  it("formats the countdown", () => {
    expect([clock(83), clock(9), clock(0), clock(-3)]).toEqual(["1:23", "0:09", "0:00", "0:00"]);
  });

  it("puts what the page needs in its address", () => {
    expect(powerHref("reboot", "boot-1", "/settings/updates")).toBe("/power?action=reboot&from=boot-1&back=%2Fsettings%2Fupdates");
  });

  it("names a step's state", () => {
    expect(stepState({ kind: "mount", name: "/var/mnt/tank", state: "failed" })).toEqual({ tone: "warn", word: "busy" });
    expect(stepState({ kind: "stack", name: "media", state: "done" })).toEqual({ tone: "ok", word: "stopped" });
  });
});
