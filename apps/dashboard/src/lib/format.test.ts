import { afterEach, describe, expect, it } from "vitest";
import { clock } from "./clock.svelte.ts";
import { clockTime, dateTime, stamp } from "./format.ts";

// 15:30 local time, whatever the zone the tests run in.
const at = new Date(2026, 10, 23, 15, 30);

describe("the clock format", () => {
  afterEach(() => {
    clock.format = "";
  });

  it("shows 24-hour times", () => {
    clock.format = "24h";
    expect(clockTime(at)).toContain("15");
    expect(stamp(at.toISOString())).toContain("15");
  });

  it("shows 12-hour times", () => {
    clock.format = "12h";
    expect(clockTime(at)).not.toContain("15");
    expect(dateTime(at, { hour: "numeric" })).not.toContain("15");
  });

  it("keeps seconds when asked", () => {
    clock.format = "24h";
    expect(clockTime(new Date(2026, 10, 23, 15, 30, 42), true)).toContain("42");
  });

  it("says nothing for a missing time", () => {
    expect(stamp(undefined)).toBe("–");
  });
});
