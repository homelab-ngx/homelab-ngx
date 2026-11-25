import { describe, expect, it } from "vitest";
import { clock } from "./clock.svelte.ts";
import {
  backupState,
  byDestination,
  covers,
  dayMarks,
  describeKeep,
  describeSchedule,
  destinationDraft,
  destinationInput,
  destinationProblem,
  draftOf,
  draftProblem,
  inputOf,
  KEEP_PRESETS,
  parseSchedule,
  percent,
  presetOf,
  toCron,
  uncovered,
  type Backup,
  type Coverage,
  type Destination,
  type Run,
  type RunItem,
  allTicked,
  newSource,
  selectableFolders,
  tickFolders,
  type SourceDraft,
} from "./backups.ts";

const run = (result: Run["result"]): Run =>
  ({ id: 1, backup_id: 1, backup_name: "b", trigger: "cron", started_at: "", finished_at: "", result, error: "", files: 0, bytes: 0 }) as Run;

const backup = (over: Partial<Backup> = {}): Backup =>
  ({
    id: 1,
    name: "Homelab",
    enabled: true,
    schedule: "0 4 * * *",
    bwlimit: "",
    transfers: 0,
    healthcheck_url: "",
    sources: [],
    destinations: [],
    last_run: null,
    last_success: null,
    next_run: null,
    overdue: false,
    running: false,
    created_at: "",
    updated_at: "",
    ...over,
  }) as Backup;

describe("schedule", () => {
  it("reads daily, weekly, manual and custom cron", () => {
    expect(parseSchedule("30 4 * * *")).toMatchObject({ mode: "daily", time: "04:30" });
    expect(parseSchedule("0 23 * * 7")).toMatchObject({ mode: "weekly", time: "23:00", weekday: 0 });
    expect(parseSchedule("")).toMatchObject({ mode: "manual" });
    expect(parseSchedule("0 */6 * * *")).toMatchObject({ mode: "custom", cron: "0 */6 * * *" });
    expect(parseSchedule("0 4 1 * *").mode).toBe("custom");
    expect(parseSchedule("99 4 * * *").mode).toBe("custom");
  });

  it("writes the cron back unchanged", () => {
    for (const c of ["30 4 * * *", "5 0 * * 3", "", "0 */6 * * *"]) expect(toCron(parseSchedule(c))).toBe(c);
    expect(toCron({ mode: "weekly", time: "07:05", weekday: 1, cron: "" })).toBe("5 7 * * 1");
  });

  it("says it in words", () => {
    clock.format = "24h";
    expect(describeSchedule("0 4 * * *")).toBe("Daily at 04:00");
    expect(describeSchedule("0 4 * * 1")).toBe("Mondays at 04:00");
    expect(describeSchedule("")).toBe("Manual");
    expect(describeSchedule("@hourly")).toBe("@hourly");
    clock.format = "12h";
    expect(describeSchedule("0 16 * * *")).not.toContain("16");
    clock.format = "";
  });
});

describe("keep", () => {
  it("names the preset its numbers are", () => {
    expect(presetOf(KEEP_PRESETS.short.keep)).toBe("short");
    expect(presetOf({ daily: 7, weekly: 4, monthly: 6, yearly: 0 })).toBe("standard");
    expect(presetOf({ daily: 7, weekly: 4, monthly: 6, yearly: 3 })).toBe("long");
    expect(presetOf({ daily: 1, weekly: 0, monthly: 0, yearly: 0 })).toBe("custom");
  });
  it("says it in words", () => {
    expect(describeKeep(KEEP_PRESETS.standard.keep)).toBe("Keeps 7 daily, 4 weekly, 6 monthly");
    expect(describeKeep({ daily: 0, weekly: 0, monthly: 0, yearly: 0 })).toBe("Keeps every snapshot");
  });
});

describe("backupState", () => {
  it("puts running first, then off, then the last result", () => {
    expect(backupState(backup({ running: true, enabled: false })).label).toBe("Running");
    expect(backupState(backup({ enabled: false, last_run: run("failure") })).label).toBe("Off");
    expect(backupState(backup({ last_run: run("failure"), overdue: true }))).toEqual({ tone: "bad", label: "Failed" });
    expect(backupState(backup({ last_run: run("partial") })).label).toBe("Partial");
    expect(backupState(backup({ last_run: run("success"), overdue: true })).label).toBe("Overdue");
    expect(backupState(backup({ last_run: run("success") }))).toEqual({ tone: "ok", label: "OK" });
    expect(backupState(backup()).label).toBe("Never run");
  });
});

describe("covers", () => {
  it("counts by kind", () => {
    expect(covers([{ kind: "core" }, { kind: "stack" }, { kind: "stack" }, { kind: "path" }])).toBe("Server settings, 2 stack folders, 1 other folder");
    expect(covers([])).toBe("Nothing yet");
  });
});

describe("progress and items", () => {
  it("has no percent while the total is unknown", () => {
    expect(percent(5, 0)).toBeNull();
    expect(percent(50, 200)).toBe(25);
    expect(percent(300, 200)).toBe(100);
  });
  it("groups a run's items under their destination", () => {
    const item = (kind: RunItem["kind"], destination: string, label = ""): RunItem => ({ kind, destination, label }) as RunItem;
    const g = byDestination([item("destination", "b2"), item("folder", "b2", "a"), item("folder", "disk", "a"), item("folder", "b2", "b")]);
    expect(g.map((x) => [x.name, !!x.destination, x.folders.length])).toEqual([
      ["b2", true, 2],
      ["disk", false, 1],
    ]);
  });
});

describe("backup draft", () => {
  it("round-trips a backup into its request", () => {
    const b = backup({
      schedule: "0 4 * * 1",
      destinations: [{ id: 3, name: "b2", kind: "s3", ready: true }],
      sources: [
        { kind: "core", stack: "", folder: "", path: "", remote_path: null, size_only: false, excludes: [], declared_excludes: [], label: "Core", host_path: "", problem: "" },
        { kind: "stack", stack: "immich", folder: "UPLOAD_DIR", path: "", remote_path: "", size_only: true, excludes: [{ pattern: "/thumbs/**", reason: "rebuilt" }], declared_excludes: [], label: "", host_path: "", problem: "" },
      ],
    });
    expect(inputOf(draftOf(b))).toEqual({
      name: "Homelab",
      enabled: true,
      schedule: "0 4 * * 1",
      bwlimit: "",
      transfers: 0,
      healthcheck_url: "",
      destination_ids: [3],
      sources: [
        { kind: "core", excludes: [], size_only: false },
        { kind: "stack", stack: "immich", folder: "UPLOAD_DIR", remote_path: "", size_only: true, excludes: [{ pattern: "/thumbs/**", reason: "rebuilt" }] },
      ],
    });
  });

  it("keeps the host's path for an empty folder at the destination, and reads / as its top", () => {
    const d = draftOf(null);
    const at = (remote: string) => inputOf({ ...d, sources: [{ kind: "path", stack: "", folder: "", path: "/pool/x", remote, sizeOnly: false, excludes: [] }] }).sources?.[0]?.remote_path;
    expect(at(" ")).toBeUndefined();
    expect(at("/")).toBe("");
    expect(at("/photos/")).toBe("photos");
    const stored = backup({ sources: [{ kind: "path", stack: "", folder: "", path: "/pool/x", remote_path: "", size_only: false, excludes: [], declared_excludes: [], label: "", host_path: "", problem: "" }] });
    expect(draftOf(stored).sources[0]!.remote).toBe("/");
  });

  it("drops blank folders and blank patterns, and defaults to daily", () => {
    const d = draftOf(null);
    expect(toCron(d.schedule)).toBe("0 4 * * *");
    d.name = " Photos ";
    d.sources.push({ kind: "path", stack: "", folder: "", path: " ", remote: "", sizeOnly: false, excludes: [] });
    d.sources.push({ kind: "path", stack: "", folder: "", path: "/pool/x", remote: "", sizeOnly: false, excludes: [{ pattern: " ", reason: "" }] });
    expect(inputOf(d).sources).toEqual([{ kind: "path", path: "/pool/x", excludes: [], size_only: false }]);
    expect(inputOf(d).name).toBe("Photos");
  });

  it("names what stops a save", () => {
    const d = draftOf(null);
    expect(draftProblem(d)).toMatch(/Name/);
    d.name = "x";
    expect(draftProblem(d)).toBe("");
    d.schedule = { mode: "custom", time: "", weekday: 0, cron: "daily" };
    expect(draftProblem(d)).toMatch(/five parts/);
  });
});

describe("uncovered", () => {
  it("lists the folders no backup includes", () => {
    const c: Coverage = {
      uncovered: 1,
      stacks: [
        {
          stack: "immich",
          prepare: "",
          folders: [
            { key: "A", label: "Library", host_path: "/p", excludes: [], problem: "", covered_by: [] },
            { key: "B", label: "Dumps", host_path: "/d", excludes: [], problem: "", covered_by: ["Homelab"] },
          ],
        },
      ],
    };
    expect(uncovered(c)).toEqual([{ stack: "immich", key: "A", label: "Library" }]);
    expect(uncovered(undefined)).toEqual([]);
  });
});

describe("destination draft", () => {
  const stored = {
    id: 1,
    name: "b2",
    kind: "s3",
    s3: { endpoint: "https://s3.example.com", region: "", bucket: "bk", prefix: "", access_key_id: "AK" },
    delete_removed: true,
    keep: { daily: 0, weekly: 0, monthly: 0, yearly: 0 },
    password_confirmed: false,
    used_by: [],
    created_at: "",
    updated_at: "",
  } as Destination;

  it("leaves a blank secret out on an edit, so the stored one stays", () => {
    const d = destinationDraft(stored);
    expect(destinationProblem(d, false)).toBe("");
    expect(destinationInput(d).s3).toEqual({ endpoint: "https://s3.example.com", region: "", bucket: "bk", prefix: "", access_key_id: "AK" });
    expect(destinationInput(d).delete_removed).toBe(true);
    expect(destinationProblem(d, true)).toMatch(/Secret access key/);
  });

  it("sends a restic password only for an existing repository, and credentials only when edited", () => {
    const d = destinationDraft(null);
    Object.assign(d, { name: "disk", kind: "restic", repository: "/mnt/repo", password: "typed" });
    d.env = [{ name: "B2_ACCOUNT_KEY", value: "k" }, { name: " ", value: "x" }];
    expect(destinationInput(d).restic).toEqual({ repository: "/mnt/repo", env: { B2_ACCOUNT_KEY: "k" } });
    expect(destinationInput(d).keep).toEqual(KEEP_PRESETS.standard.keep);
    d.existing = true;
    expect(destinationInput(d).restic?.password).toBe("typed");
    d.env = null;
    expect(destinationInput(d).restic).not.toHaveProperty("env");
  });
});

describe("dayMarks", () => {
  const r = (id: number, started: Date, result: Run["result"], bytes = 0): Run => ({
    id,
    backup_id: 1,
    backup_name: "b",
    trigger: "cron",
    started_at: started.toISOString(),
    finished_at: result === "running" ? null : new Date(started.getTime() + 90_000).toISOString(),
    result,
    error: "",
    files: 0,
    bytes,
  });
  const now = new Date(2026, 10, 16, 12, 0);

  it("gives 14 days, oldest first, today last", () => {
    const m = dayMarks([], now);
    expect(m).toHaveLength(14);
    expect(m[0]!.key).toBe("2026-11-03");
    expect(m[13]!.key).toBe("2026-11-16");
    expect(m.every((d) => d.tone === "none" && d.run === null)).toBe(true);
    expect(m[13]!.label).toBe("16 Nov: did not run");
  });

  it("puts a run on the local day it started", () => {
    // 23:30 and 00:30 local are different days whatever the UTC date is.
    const m = dayMarks([r(1, new Date(2026, 10, 14, 23, 30), "success"), r(2, new Date(2026, 10, 15, 0, 30), "failure")], now);
    expect(m.find((d) => d.key === "2026-11-14")!.run!.id).toBe(1);
    expect(m.find((d) => d.key === "2026-11-15")!.run!.id).toBe(2);
  });

  it("takes the day's worst result and opens its newest run", () => {
    const day = (h: number) => new Date(2026, 10, 15, h);
    const m = dayMarks([r(3, day(20), "success"), r(1, day(4), "failure"), r(2, day(12), "partial")], now);
    const d = m.find((x) => x.key === "2026-11-15")!;
    expect(d.tone).toBe("bad");
    expect(d.run!.id).toBe(3);
    expect(d.label).toBe("15 Nov: Failed, 3 runs");
  });

  it("describes a single run with how long it took and how much it sent", () => {
    const d = dayMarks([r(1, new Date(2026, 10, 16, 4), "success", 212_000_000)], now)[13]!;
    expect(d).toMatchObject({ tone: "ok", label: "16 Nov: OK, 1 min 30 s, 212 MB" });
  });

  it("counts unknown runs as failed, partial as partly failed, and a stopped run as neither", () => {
    const tone = (res: Run["result"]) => dayMarks([r(1, new Date(2026, 10, 16, 4), res)], now)[13]!.tone;
    expect([tone("cancelled"), tone("unknown"), tone("failure"), tone("partial")]).toEqual(["warn", "bad", "bad", "warn"]);
    const day = (h: number) => new Date(2026, 10, 16, h);
    expect(dayMarks([r(1, day(4), "cancelled")], now)[13]!.label).toMatch(/^16 Nov: Stopped/);
    expect(dayMarks([r(1, day(4), "cancelled"), r(2, day(5), "partial")], now)[13]!.label).toBe("16 Nov: Partly failed, 2 runs");
    expect(dayMarks([r(1, day(4), "cancelled"), r(2, day(5), "failure")], now)[13]!.label).toBe("16 Nov: Failed, 2 runs");
  });

  it("shows a run in flight, but not over a failure the same day", () => {
    const day = (h: number) => new Date(2026, 10, 16, h);
    expect(dayMarks([r(1, day(4), "success"), r(2, day(11), "running")], now)[13]).toMatchObject({ tone: "run", label: "16 Nov: Running, 2 runs" });
    expect(dayMarks([r(2, day(11), "running")], now)[13]!.label).toBe("16 Nov: Running");
    expect(dayMarks([r(1, day(4), "failure"), r(2, day(11), "running")], now)[13]!.tone).toBe("bad");
  });

  it("keeps 14 distinct days across a clock change", () => {
    // Europe's clocks go back on 25 Oct 2026 and forward on 28 Mar 2027; the
    // count is on the calendar, so the time zone of the test run does not matter.
    for (const end of [new Date(2026, 9, 30, 0, 30), new Date(2027, 3, 2, 23, 30), new Date(2026, 10, 5, 12)]) {
      const keys = dayMarks([], end).map((d) => d.key);
      expect(new Set(keys).size).toBe(14);
      expect(keys[13]).toBe(`${end.getFullYear()}-${String(end.getMonth() + 1).padStart(2, "0")}-${String(end.getDate()).padStart(2, "0")}`);
    }
  });

  it("leaves out runs older than the window", () => {
    expect(dayMarks([r(1, new Date(2026, 9, 1, 4), "failure")], now).every((d) => d.tone === "none")).toBe(true);
  });
});

describe("ticking recommended folders", () => {
  const stacks: Coverage["stacks"] = [
    {
      stack: "media",
      prepare: "",
      folders: [
        { key: "/a", label: "A", host_path: "/x/a", excludes: [], problem: "", covered_by: [] },
        { key: "/b", label: "B", host_path: "/x/b", excludes: [], problem: "", covered_by: [] },
        { key: "GONE", label: "Gone", host_path: "", excludes: [], problem: "the variable has no value", covered_by: [] },
      ],
    },
    { stack: "peer", prepare: "", folders: [{ key: "/p", label: "P", host_path: "/x/p", excludes: [], problem: "", covered_by: [] }] },
  ];
  const keys = (list: SourceDraft[]) => list.map((s) => (s.kind === "stack" ? `${s.stack}:${s.folder}` : s.kind));

  it("leaves out a folder that cannot be found", () => {
    expect(selectableFolders(stacks)).toEqual([
      { stack: "media", key: "/a" },
      { stack: "media", key: "/b" },
      { stack: "peer", key: "/p" },
    ]);
  });

  it("selects all, keeping what was ticked and the other sources", () => {
    const core = newSource("core");
    const a = newSource("stack", { stack: "media", folder: "/a", sizeOnly: true });
    const path = newSource("path", { path: "/pool/docs" });
    const all = selectableFolders(stacks);
    const out = tickFolders([core, a, path], all, true, new Map());
    expect(keys(out)).toEqual(["core", "media:/a", "path", "media:/b", "peer:/p"]);
    expect(out[1]).toBe(a);
    expect(allTicked(out, all)).toBe(true);
    expect(allTicked([core, a], all)).toBe(false);
    expect(allTicked(out, [])).toBe(false);
  });

  it("deselects one stack and gives a folder its settings back when it is ticked again", () => {
    const parked = new Map<string, SourceDraft>();
    const a = newSource("stack", { stack: "media", folder: "/a", excludes: [{ pattern: "/tmp/**", reason: "scratch" }] });
    const p = newSource("stack", { stack: "peer", folder: "/p" });
    const core = newSource("core");
    const media = selectableFolders(stacks).filter((f) => f.stack === "media");
    const off = tickFolders([core, a, p], media, false, parked);
    expect(keys(off)).toEqual(["core", "peer:/p"]);
    const on = tickFolders(off, media, true, parked);
    expect(on.find((s) => s.folder === "/a")).toBe(a);
    expect(keys(on)).toEqual(["core", "peer:/p", "media:/a", "media:/b"]);
  });
});
