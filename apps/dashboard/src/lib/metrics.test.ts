import { describe, expect, it } from "vitest";
import {
  appendFrames,
  cpuShort,
  machineLine,
  uptime,
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
  power_cpu: at.map(() => 5),
  power_wall: at.map(() => 15),
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
    expect(mood(withPressure(0, 0, 0))).toEqual({ mood: "idle", share: 5 });
    expect(mood(withPressure(0, 0, 0, 40))).toEqual({ mood: "light", share: 40 });
    expect(mood(emptyFrames())).toBeUndefined();
  });
  it("names what work waits for", () => {
    expect(mood(withPressure(12, 0, 0))).toEqual({ mood: "medium", waits: "cpu", share: 12 });
    expect(mood(withPressure(0, 0, 12))).toEqual({ mood: "medium", waits: "disks", share: 12 });
    expect(mood(withPressure(0, 15, 0))).toEqual({ mood: "heavy", waits: "memory", share: 15 });
    // Disks are further over their line (3x) than CPU (1.2x).
    expect(mood(withPressure(12, 0, 30))).toEqual({ mood: "heavy", waits: "disks", share: 30 });
  });
  it("counts 80% CPU use as medium load", () => {
    expect(mood(withPressure(0, 0, 0, 90))).toEqual({ mood: "medium", share: 90 });
  });
});

describe("the machine line", () => {
  it("shortens processor names", () => {
    expect(cpuShort("Intel(R) Core(TM) i5-8259U CPU @ 2.30GHz")).toBe("Intel Core i5-8259U");
    expect(cpuShort("AMD Ryzen 7 5700G with Radeon Graphics")).toBe("AMD Ryzen 7 5700G");
    expect(cpuShort("AMD Ryzen 5 3600 6-Core Processor")).toBe("AMD Ryzen 5 3600");
    expect(cpuShort("Cortex-A76")).toBe("Cortex-A76");
  });
  it("counts uptime in its largest unit", () => {
    expect(uptime(0, 6.5 * 86400 * 1000)).toBe("6 d");
    expect(uptime(0, 3 * 3600 * 1000 + 5)).toBe("3 h");
    expect(uptime(0, 12 * 60 * 1000)).toBe("12 min");
  });
  it("leaves out what the host does not say", () => {
    const info = { cores: 16, memory_total: 32e9, booted_at: 0, model: "", cpu: "" };
    expect(machineLine(info, 3600 * 1000)).toBe("16 threads · 32.0 GB");
    expect(machineLine({ ...info, model: "ASRock B550M Pro4", cpu: "AMD Ryzen 7 5700G with Radeon Graphics", booted_at: 1 }, 2 * 86400 * 1000)).toBe(
      "ASRock B550M Pro4 · AMD Ryzen 7 5700G, 16 threads · 32.0 GB · up 1 d",
    );
  });
});
