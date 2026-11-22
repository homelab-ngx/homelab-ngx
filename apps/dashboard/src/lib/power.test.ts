import { describe, expect, it } from "vitest";
import { clock, milestones, powerHref, powerView, stepState, underWay, type PowerInput, type PowerStatus } from "./power.ts";

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

  it("is ready once the daemon has handed the host over", () => {
    expect(powerView(input({ status: status({ phase: "going_down" }) })).stage).toBe("ready");
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

  it("does not guess at a step the host left unfinished", () => {
    const step = { kind: "mount", name: "/var/mnt/tank", state: "working" } as const;
    expect(stepState(step)).toEqual({ tone: "info", word: "unmounting" });
    expect(stepState(step, true)).toEqual({ tone: "idle", word: "not seen" });
    expect(stepState({ ...step, state: "done" }, true)).toEqual({ tone: "ok", word: "unmounted" });
  });
});

describe("milestones", () => {
  const states = (m: ReturnType<typeof milestones>) => m.map((x) => `${x.label}:${x.state}`);

  it("marks the step under way", () => {
    expect(states(milestones("reboot", "stopping", "unmounting", false))).toEqual([
      "stacks stopped:done",
      "pools unmounted:active",
      "rebooting:waiting",
      "back:waiting",
    ]);
  });

  it("follows a reboot that was seen ready through to the host back", () => {
    expect(states(milestones("reboot", "ready", "going_down", true))).toEqual(["stacks stopped:done", "pools unmounted:done", "rebooting:active", "back:waiting"]);
    expect(states(milestones("reboot", "down", "going_down", true))).toEqual(["stacks stopped:done", "pools unmounted:done", "rebooting:active", "back:waiting"]);
    expect(states(milestones("reboot", "back", "going_down", true))).toEqual(["stacks stopped:done", "pools unmounted:done", "rebooting:done", "back:done"]);
  });

  it("leaves what it did not see waiting", () => {
    expect(states(milestones("reboot", "down", "unmounting", false))).toEqual(["stacks stopped:done", "pools unmounted:waiting", "rebooting:active", "back:waiting"]);
    expect(states(milestones("reboot", "down", undefined, false))).toEqual(["stacks stopped:waiting", "pools unmounted:waiting", "rebooting:active", "back:waiting"]);
  });

  it("ends a poweroff at off", () => {
    expect(states(milestones("poweroff", "ready", "going_down", true))).toEqual(["stacks stopped:done", "pools unmounted:done", "off:active"]);
    expect(states(milestones("poweroff", "off", "going_down", true))).toEqual(["stacks stopped:done", "pools unmounted:done", "off:done"]);
  });
});
