import { describe, expect, it } from "vitest";
import { deployRows, type CoreDeploy } from "./components/core/core.ts";
import { clock, holdup, phaseRows, powerHref, powerRows, powerView, stepState, timer, titleOf, underWay, type PhaseInput, type PowerInput, type PowerStatus } from "./power.ts";

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

describe("phaseRows", () => {
  const rows = (p: Partial<PhaseInput>) =>
    phaseRows({ action: "reboot", stage: "stopping", phase: "stopping_stacks", sawReady: false, steps: [], ...p }).map((r) => `${r.indent ? "  " : ""}${r.name}:${r.word}`);
  const stacks = (...states: ("waiting" | "working" | "done" | "failed")[]) => states.map((state, n) => ({ kind: "stack" as const, name: `s${n}`, state }));

  it("lists every phase from the start", () => {
    expect(rows({ stage: "starting", phase: undefined })).toEqual(["Stop stacks:starting", "Unmount pools:waiting", "Reboot:waiting", "Back online:waiting"]);
    expect(rows({ stage: "starting", phase: undefined, install: { past: false, reachable: true }, to: "0.5.0" })).toEqual([
      "Install:starting",
      "Stop stacks:waiting",
      "Unmount pools:waiting",
      "Reboot:waiting",
      "Back on 0.5.0:waiting",
    ]);
  });

  it("counts the stacks stopped, and the pools once they unmount", () => {
    expect(rows({ steps: stacks("done", "working", "waiting") })[0]).toBe("Stop stacks:1 of 3");
    const mounts = [{ kind: "mount" as const, name: "/var/mnt/tank", state: "working" as const }];
    expect(rows({ phase: "unmounting", steps: [...stacks("done", "done"), ...mounts] }).slice(0, 2)).toEqual(["Stop stacks:done", "Unmount pools:0 of 1"]);
  });

  it("gives a stack slow to stop a row of its own, with the kill countdown", () => {
    const slow = { kind: "stack" as const, name: "players", state: "working" as const, waiting_on: { unit: "plex.service", kill_in: 85 } };
    const out = phaseRows({ action: "reboot", stage: "stopping", phase: "stopping_stacks", sawReady: false, steps: [...stacks("done", "working"), slow] });
    expect(out[1]).toMatchObject({ name: "players", indent: true, word: "stopping", wait: { when: "killed in 1:25" } });
    expect(out).toHaveLength(5);
  });

  it("keeps a stack that was killed under its group", () => {
    expect(rows({ phase: "unmounting", steps: stacks("done", "failed") }).slice(0, 2)).toEqual(["Stop stacks:1 killed", "  s1:killed"]);
  });

  it("follows the reboot to the host back", () => {
    expect(rows({ stage: "down", phase: "going_down", sawReady: true, steps: stacks("done") })).toEqual(["Stop stacks:done", "Unmount pools:done", "Reboot:rebooting", "Back online:waiting"]);
    expect(rows({ stage: "late", phase: "going_down", sawReady: true }).at(-2)).toBe("Reboot:late");
    expect(rows({ stage: "back", phase: "going_down", sawReady: true, to: "0.5.0" }).slice(-2)).toEqual(["Reboot:done", "Back on 0.5.0:back"]);
  });

  it("does not guess at a phase the host left unfinished", () => {
    expect(rows({ stage: "down", phase: "unmounting", steps: stacks("done") }).slice(0, 2)).toEqual(["Stop stacks:done", "Unmount pools:not seen"]);
  });

  it("ends a poweroff at off", () => {
    expect(rows({ action: "poweroff", stage: "off", phase: "going_down", sawReady: true }).at(-1)).toBe("Power off:off");
  });

  it("marks what failed", () => {
    expect(rows({ stage: "failed", phase: "failed" })[1]).toBe("Unmount pools:failed");
    expect(rows({ stage: "refused", phase: undefined })[0]).toBe("Stop stacks:failed");
    expect(rows({ stage: "install_failed", install: { past: false, reachable: true } })[0]).toBe("Install:failed");
  });

  it("puts the install first, and keeps the rest waiting while it runs", () => {
    const d = install();
    expect(rows({ stage: "installing", phase: "idle", install: { deploy: d, past: false, reachable: true } }).slice(0, 2)).toEqual(["Install:installing", "Stop stacks:waiting"]);
    expect(rows({ stage: "installing", phase: "idle", install: { deploy: d, past: false, reachable: false } })[0]).toBe("Install:restarting");
    expect(rows({ stage: "stopping", install: { past: true, reachable: true }, steps: stacks("working") }).slice(0, 2)).toEqual(["Install:done", "Stop stacks:0 of 1"]);
  });
});

describe("titleOf", () => {
  it("says what the system is doing, and that it is away", () => {
    expect(titleOf("installing", "reboot", true)).toBe("Updating");
    expect(titleOf("stopping", "reboot", false)).toBe("Rebooting");
    expect(titleOf("down", "reboot", false)).toBe("Rebooting");
    expect(titleOf("late", "reboot", false)).toBe("Offline");
    expect(titleOf("back", "reboot", false)).toBe("Back online");
    expect(titleOf("stopping", "poweroff", false)).toBe("Shutting down");
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
    const words = (d: CoreDeploy | undefined, away: boolean) => deployRows(d, away).map((r) => `${r.name}:${r.word}`);
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

describe("the timer and the rows", () => {
  const at = { since: 40_000, now: 100_000, expected: 90 };
  it("counts up, then down while the host is away, then the time over", () => {
    expect(timer(powerView(input()), at)).toEqual({ value: "1:00", label: "elapsed" });
    const down = powerView(input({ reachable: false, downSince: 70_000 }));
    expect(timer(down, { ...at, downSince: 70_000 })).toEqual({ value: "1:30", label: "left" });
    const late = powerView(input({ reachable: false, downSince: -40_000 }));
    expect(timer(late, { ...at, downSince: -40_000 })).toEqual({ value: "+0:20", label: "over" });
  });

  it("shows a stack's holdup on its row, and nothing once the host is gone", () => {
    const step = { kind: "stack" as const, name: "players", state: "working" as const, waiting_on: { unit: "plex.service", kill_in: 5 } };
    expect(powerRows([step])[0]).toMatchObject({ bar: "run", word: "stopping", wait: { what: "Waiting for plex.service", when: "killed in 0:05" } });
    expect(powerRows([step], true)[0]).toMatchObject({ bar: "unknown", word: "not seen", wait: undefined });
  });
});
