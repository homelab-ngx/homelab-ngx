import { describe, expect, it } from "vitest";
import type { CoreDeploy } from "./components/core/core.ts";
import { clock, holdup, installRows, powerHref, powerRows, powerView, probe, stepState, timer, trackOf, underWay, type PowerInput, type PowerStatus } from "./power.ts";

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
  checked: true,
  now: 100_000,
  expected: 90,
  ...p,
});

describe("powerView", () => {
  it("shows the steps while the daemon stops the host", () => {
    const v = powerView(input());
    expect([v.stage, v.steps.length, v.left]).toEqual(["stopping", 1, 120]);
  });

  it("counts down from when the host went away, 30 s past the last reboot", () => {
    const v = powerView(input({ reachable: false, downSince: 70_000 }));
    expect([v.stage, v.left, v.done]).toEqual(["down", 90, 1 / 4]);
    // The steps it last showed stay on the page.
    expect(v.steps).toHaveLength(1);
  });

  it("keeps checking past the countdown", () => {
    const v = powerView(input({ reachable: false, downSince: -20_000 }));
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
    expect([v.stage, v.left]).toEqual(["down", 120]);
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

describe("holdup", () => {
  const plex = { kind: "stack" as const, name: "players", state: "working" as const, waiting_on: { unit: "plex.service", description: "Plex Media Server", kill_in: 58 } };

  it("names the unit and counts down to its kill", () => {
    expect(holdup(plex)).toEqual({ what: "Waiting for Plex Media Server (plex.service)", when: "killed in 0:58" });
    expect(holdup({ ...plex, waiting_on: { unit: "homelab-stack-players.service", kill_in: 0 } })).toEqual({
      what: "Waiting for homelab-stack-players.service",
      when: "killing",
    });
  });

  it("says nothing once the stack stopped or the host is gone", () => {
    expect(holdup({ ...plex, state: "done" })).toBeUndefined();
    expect(holdup(plex, true)).toBeUndefined();
    expect(holdup({ ...plex, waiting_on: undefined })).toBeUndefined();
  });
});

describe("trackOf", () => {
  const states = (m: ReturnType<typeof trackOf>) => m.map((x) => `${x.label}:${x.state}`);

  it("marks the step under way", () => {
    expect(states(trackOf("reboot", "stopping", "unmounting", false))).toEqual(["stacks:done", "pools:active", "reboot:waiting", "back:waiting"]);
  });

  it("follows a reboot that was seen ready through to the host back", () => {
    expect(states(trackOf("reboot", "ready", "going_down", true))).toEqual(["stacks:done", "pools:done", "reboot:active", "back:waiting"]);
    expect(states(trackOf("reboot", "down", "going_down", true))).toEqual(["stacks:done", "pools:done", "reboot:active", "back:waiting"]);
    expect(states(trackOf("reboot", "back", "going_down", true))).toEqual(["stacks:done", "pools:done", "reboot:done", "back:done"]);
  });

  it("leaves what it did not see waiting", () => {
    expect(states(trackOf("reboot", "down", "unmounting", false))).toEqual(["stacks:done", "pools:waiting", "reboot:active", "back:waiting"]);
    expect(states(trackOf("reboot", "down", undefined, false))).toEqual(["stacks:waiting", "pools:waiting", "reboot:active", "back:waiting"]);
  });

  it("ends a poweroff at off", () => {
    expect(states(trackOf("poweroff", "ready", "going_down", true))).toEqual(["stacks:done", "pools:done", "off:active"]);
    expect(states(trackOf("poweroff", "off", "going_down", true))).toEqual(["stacks:done", "pools:done", "off:done"]);
  });

  it("starts at the first stage, and crosses out the one that failed", () => {
    expect(states(trackOf("reboot", "starting", undefined, false))[0]).toBe("stacks:active");
    expect(states(trackOf("reboot", "refused", undefined, false))[0]).toBe("stacks:failed");
    expect(states(trackOf("reboot", "failed", "unmounting", false)).slice(0, 2)).toEqual(["stacks:done", "pools:failed"]);
  });

  it("puts an update's install first", () => {
    expect(states(trackOf("reboot", "installing", undefined, false, { past: false }))).toEqual(["update:active", "stacks:waiting", "pools:waiting", "reboot:waiting", "back:waiting"]);
    expect(states(trackOf("reboot", "install_failed", undefined, false, { past: false }))[0]).toBe("update:failed");
    expect(states(trackOf("reboot", "starting", "idle", false, { past: true })).slice(0, 2)).toEqual(["update:done", "stacks:active"]);
    expect(states(trackOf("reboot", "back", "going_down", true, { past: true }))).toEqual(["update:done", "stacks:done", "pools:done", "reboot:done", "back:done"]);
  });
});

const install = (p: Partial<CoreDeploy> = {}): CoreDeploy => ({
  id: 7,
  kind: "install",
  version: "0.5.0",
  rev: "",
  phase: "Update",
  reason: "",
  result: "running",
  log: "",
  failed_units: [],
  started_at: "2026-11-23T10:00:00Z",
  finished_at: null,
  ...p,
});

describe("the request and the install", () => {
  it("is starting until the daemon answers, and refused when it says no", () => {
    expect(powerView(input({ checked: false, status: undefined, reachable: false })).stage).toBe("starting");
    expect(powerView(input({ status: status({ phase: "idle" }) })).stage).toBe("starting");
    const v = powerView(input({ start: { pending: false, error: "An update is already running." } }));
    expect([v.stage, v.error]).toEqual(["refused", "An update is already running."]);
  });

  it("follows the install before the shutdown", () => {
    const idle = status({ phase: "idle", steps: [] });
    expect(powerView(input({ status: idle, install: { past: false } })).stage).toBe("starting");
    expect(powerView(input({ status: idle, install: { deploy: install(), past: false } })).stage).toBe("installing");
    // The install's restart is not the reboot: no countdown starts.
    expect(powerView(input({ status: idle, reachable: false, install: { deploy: install({ phase: "Restart" }), past: false } })).stage).toBe("installing");
    expect(powerView(input({ status: idle, install: { deploy: install({ result: "healthy" }), past: false } })).stage).toBe("starting");
  });

  it("stops at an install that went back", () => {
    const v = powerView(input({ install: { deploy: install({ result: "rolled_back", phase: "Roll back" }), past: false } }));
    expect([v.stage, v.error]).toEqual(["install_failed", "Didn't come up, so the homelab went back to the version before"]);
  });

  it("names the install's steps, the restart included", () => {
    const words = (d: CoreDeploy | undefined, away: boolean) => installRows(d, away).map((r) => `${r.name}:${r.word}`);
    expect(words(install({ phase: "Restart" }), true)).toEqual(["Update:done", "Restart:restarting", "Health check:waiting"]);
    expect(words(install({ phase: "Update" }), true)).toEqual(["Update:done", "Restart:restarting", "Health check:waiting"]);
    expect(words(install({ result: "healthy", phase: "Installed 0.5.0; rebooting" }), false)).toEqual(["Update:done", "Restart:done", "Health check:done"]);
    expect(words(install({ result: "rolled_back", phase: "Roll back" }), false)).toEqual(["Update:done", "Restart:done", "Health check:failed"]);
  });
});

describe("the overall bar", () => {
  const share = (p: Partial<PowerInput>) => powerView(input(p)).share;

  it("moves forward through every stage", () => {
    const shares = [
      share({ checked: false, status: undefined, reachable: false, install: { past: false } }),
      share({ install: { deploy: install({ phase: "Restart" }), past: false } }),
      share({ install: { deploy: install({ result: "healthy" }), past: true }, status: status({ phase: "stopping_stacks" }) }),
      share({ install: { past: true }, status: status({ phase: "unmounting", steps: [{ kind: "mount", name: "/var/mnt/tank", state: "working" }] }) }),
      share({ install: { past: true }, status: status({ phase: "going_down" }) }),
      share({ install: { past: true }, reachable: false, downSince: 70_000 }),
      share({ install: { past: true }, reachable: false, downSince: -100_000 }),
      share({ install: { past: true }, status: status({ boot_id: "boot-2", phase: "idle" }) }),
    ];
    for (let n = 1; n < shares.length; n++) expect(shares[n]).toBeGreaterThan(shares[n - 1]!);
    expect(shares.at(-1)).toBe(1);
  });

  it("gives the countdown most of the bar", () => {
    expect(share({ reachable: false, downSince: 70_000 })).toBeCloseTo(0.3 + 0.67 / 4);
  });
});

describe("the timer and the checks", () => {
  const at = { since: 40_000, now: 100_000, expected: 90 };
  it("counts up, then down while the host is away, then the time over", () => {
    expect(timer(powerView(input()), at)).toEqual({ value: "1:00", label: "elapsed" });
    const down = powerView(input({ reachable: false, downSince: 70_000 }));
    expect(timer(down, { ...at, downSince: 70_000 })).toEqual({ value: "1:30", label: "left" });
    const late = powerView(input({ reachable: false, downSince: -40_000 }));
    expect(timer(late, { ...at, downSince: -40_000 })).toEqual({ value: "+0:20", label: "over" });
  });

  it("keeps the last checks", () => {
    let list = probe([], "miss", 3);
    list = probe(probe(probe(list, "miss", 3), "miss", 3), "back", 3);
    expect(list).toEqual(["miss", "miss", "back"]);
  });

  it("shows a stack's holdup on its row, and nothing once the host is gone", () => {
    const step = { kind: "stack" as const, name: "players", state: "working" as const, waiting_on: { unit: "plex.service", kill_in: 5 } };
    expect(powerRows([step])[0]).toMatchObject({ bar: "run", word: "stopping", wait: { what: "Waiting for plex.service", when: "killed in 0:05" } });
    expect(powerRows([step], true)[0]).toMatchObject({ bar: "unknown", word: "not seen", wait: undefined });
  });
});
