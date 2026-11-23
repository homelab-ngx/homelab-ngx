import { describe, expect, it } from "vitest";
import { current, NAV, wide } from "./nav.ts";

describe("current", () => {
  it("keeps the overview and the stacks apart", () => {
    expect(current("/")).toBe("/");
    expect(current("/stacks")).toBe("/stacks");
    expect(current("/stacks/immich")).toBe("/stacks");
    expect(current("/stacks/immich/logs")).toBe("/stacks");
    expect(current("/settings/logs")).toBe("/settings");
  });
  it("has no Notifications page: the bell's tray and Settings hold it", () => {
    expect(NAV.map((n) => n.href)).not.toContain("/notifications");
    expect(current("/settings/notifications")).toBe("/settings");
  });
});

describe("wide", () => {
  it("is a draft's editor and a stack's source code, nothing else", () => {
    expect(wide("/stacks/drafts/mine")).toBe(true);
    expect(wide("/stacks/immich/source")).toBe(true);
    expect(wide("/storage/edit")).toBe(true);
    expect(wide("/storage/browse")).toBe(false);
    expect(wide("/stacks/immich")).toBe(false);
    expect(wide("/stacks/immich/logs")).toBe(false);
    expect(wide("/stacks")).toBe(false);
  });
});
