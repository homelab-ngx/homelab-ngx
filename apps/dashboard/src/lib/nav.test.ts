import { describe, expect, it } from "vitest";
import { current } from "./nav.ts";

describe("current", () => {
  it("keeps the overview and the stacks apart", () => {
    expect(current("/")).toBe("/");
    expect(current("/stacks")).toBe("/stacks");
    expect(current("/stacks/immich")).toBe("/stacks");
    expect(current("/stacks/immich/logs")).toBe("/stacks");
    expect(current("/settings/logs")).toBe("/settings");
  });
});
