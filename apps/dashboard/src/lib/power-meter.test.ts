import { describe, expect, it } from "vitest";
import { newHost, powerMeter, powerProfiles } from "../demo/host.ts";
import { compareRows, meterState, profileMarks, span } from "./power-meter.ts";

describe("power meter", () => {
  it("says where the connection stands", () => {
    const h = newHost(() => 0);
    expect(meterState(powerMeter(h))).toEqual({ tone: "ok", word: "connected" });
    expect(meterState({ ...powerMeter(h), state: "auth_failed" })).toEqual({ tone: "bad", word: "refused" });
    h.meter = { url: "", entity: "", has_token: false, idle_only: false };
    expect(meterState(powerMeter(h)).word).toBe("off");
  });

  it("writes a stretch of minutes", () => {
    expect(span(45)).toBe("45 min");
    expect(span(150)).toBe("3 h");
    expect(span(3 * 1440)).toBe("3 d");
  });

  it("compares each profile with Balanced at the wall", () => {
    const rows = compareRows(powerProfiles(newHost(() => 0), "7d", false).profiles);
    expect(rows.map((r) => r.name)).toEqual(["Power saver", "Balanced"]);
    expect(rows[0]).toEqual({ name: "Power saver", time: "24 h", wall: "20 W", cpu: "6.9 W", delta: "−4.4 W" });
    expect(rows[1]!.delta).toBe("");
  });

  it("compares at the CPU package without a meter", () => {
    const rows = compareRows([
      { profile: "balanced", minutes: 60, wall_minutes: 0, wall: null, cpu: 2.8 },
      { profile: "power-saver", minutes: 60, wall_minutes: 0, wall: null, cpu: 0.9 },
      { profile: "performance", minutes: 60, wall_minutes: 0, wall: null, cpu: 4.1 },
    ]);
    expect(rows.map((r) => [r.name, r.wall, r.delta])).toEqual([
      ["Power saver", "–", "−1.9 W"],
      ["Balanced", "–", ""],
      ["Performance", "–", "+1.3 W"],
    ]);
  });

  it("marks the profile changes by name", () => {
    expect(profileMarks([{ at: 60, profile: "power-saver" }])).toEqual([{ at: 60, label: "Power saver" }]);
    expect(profileMarks(undefined)).toEqual([]);
  });
});
