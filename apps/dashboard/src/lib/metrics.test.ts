import { describe, expect, it } from "vitest";
import {
  appendFrames,
  byScope,
  CORE_HUE,
  emptyFrames,
  mood,
  OTHER_HUE,
  PARTS,
  stackHues,
  STACK_HUES,
  type Frames,
  type Series,
} from "./metrics.ts";

const series = (v: (number | null)[], over: Partial<Series> = {}): Series => ({
  cpu: v,
  memory: v,
  pressure_cpu: v,
  pressure_memory: v,
  pressure_io: v,
  disk_read: v,
  disk_write: v,
  net_rx: v,
  net_tx: v,
  ...over,
});

const frames = (at: number[], stacks: Record<string, (number | null)[]> = {}): Frames => ({
  at,
  step: 2,
  host: series(at.map(() => 1)),
  services: series(at.map(() => 1)),
  sessions: series(at.map(() => 3)),
  kernel: series(at.map(() => 4)),
  stacks: Object.fromEntries(Object.entries(stacks).map(([k, v]) => [k, series(v)])),
  temp_cpu: at.map(() => 40),
  temp_disk: at.map(() => 30),
});

describe("appendFrames", () => {
  it("pads a stack that appears or goes away with nulls", () => {
    const out = appendFrames(frames([0, 2], { a: [5, 6] }), frames([4], { b: [7] }));
    expect(out.at).toEqual([0, 2, 4]);
    expect(out.stacks.a?.cpu).toEqual([5, 6, null]);
    expect(out.stacks.b?.cpu).toEqual([null, null, 7]);
  });

  it("keeps only the window and drops a stack with nothing left in it", () => {
    const out = appendFrames(frames([0, 2], { a: [5, 6] }), frames([10], {}), 8);
    expect(out.at).toEqual([2, 10]);
    expect(out.host.cpu).toEqual([1, 1]);
    expect(out.stacks.a?.cpu).toEqual([6, null]);
    const later = appendFrames(out, frames([20]), 8);
    expect(later.stacks.a).toBeUndefined();
  });

  it("starts from nothing", () => {
    expect(appendFrames(emptyFrames(), frames([4])).at).toEqual([4]);
  });
});

describe("byScope", () => {
  it("puts stacks by name first, folding the ones past the palette, then the parts of core", () => {
    const names = ["a", "b", "c", "d", "e", "f", "g", "h"];
    const f = frames([0], Object.fromEntries(names.map((n) => [n, [1]])));
    const lines = byScope(f, "cpu", stackHues(names));
    expect(lines.map((l) => l.label)).toEqual([
      "a",
      "b",
      "c",
      "d",
      "e",
      "f",
      "Other",
      "Core (System Services)",
      "Core (User Sessions)",
      "Core (Kernel)",
    ]);
    expect(lines.slice(-3).map((l) => l.values)).toEqual([[1], [3], [4]]);
    expect(lines.at(-3)?.hue).toBe(CORE_HUE);
    expect(lines[6]?.hue).toBe(OTHER_HUE);
    expect(lines[6]?.values).toEqual([2]);
  });

  it("carries each line's series, so a stack named like a part is still itself", () => {
    const lines = byScope(frames([0], { "Core (System Services)": [9] }), "cpu", stackHues(["Core (System Services)"]));
    expect(lines[0]?.series?.cpu).toEqual([9]);
    expect(lines[1]?.series?.cpu).toEqual([1]);
  });

  it("labels a stack with its title, and keeps its link and hue on the name", () => {
    const hues = stackHues(["media"]);
    const line = byScope(frames([0], { media: [1] }), "cpu", hues, (n) => (n === "media" ? "Movies & TV" : n))[0];
    expect(line?.label).toBe("Movies & TV");
    expect(line?.href).toBe("/stacks/media");
    expect(line?.hue).toBe(hues.get("media"));
  });

  it("gives every part its own hue, none of them a stack's", () => {
    const hues = PARTS.map((p) => p.hue);
    expect(new Set(hues).size).toBe(hues.length);
    for (const h of hues) expect(STACK_HUES).not.toContain(h);
    expect(hues).not.toContain(OTHER_HUE);
  });

  it("gives a stack the same hue whichever stacks are in the chart", () => {
    const hues = stackHues(["media", "immich", "email"]);
    expect(hues.get("email")).toBe(STACK_HUES[0]);
    expect(byScope(frames([0], { media: [1] }), "cpu", hues)[0]?.hue).toBe(STACK_HUES[2]);
  });
});

describe("mood", () => {
  const at = Array.from({ length: 30 }, (_, i) => i * 2);
  const withPressure = (cpu: number, mem: number, io: number, use = 5): Frames => ({
    ...frames(at),
    host: series(at.map(() => use), {
      pressure_cpu: at.map(() => cpu),
      pressure_memory: at.map(() => mem),
      pressure_io: at.map(() => io),
    }),
  });
  it("is idle or light load while nothing waits", () => {
    expect(mood(withPressure(0, 0, 0))).toEqual({ mood: "idle" });
    expect(mood(withPressure(0, 0, 0, 40))).toEqual({ mood: "light" });
    expect(mood(emptyFrames())).toBeUndefined();
  });
  it("names what work waits for", () => {
    expect(mood(withPressure(12, 0, 0))).toEqual({ mood: "medium", waits: "processor" });
    expect(mood(withPressure(0, 0, 12))).toEqual({ mood: "medium", waits: "disks" });
    expect(mood(withPressure(0, 15, 0))).toEqual({ mood: "heavy", waits: "memory" });
    // Disks are further over their line (3x) than the processor (1.2x).
    expect(mood(withPressure(12, 0, 30))).toEqual({ mood: "heavy", waits: "disks" });
  });
  it("calls a full processor nobody waits for medium load", () => {
    expect(mood(withPressure(0, 0, 0, 90))).toEqual({ mood: "medium" });
  });
});
