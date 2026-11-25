import { describe, expect, it } from "vitest";
import { movedTile, SECTIONS, tileLabel, type Tile } from "./settings.ts";

const byKey = (k: string): Tile => SECTIONS.flatMap((s) => s.tiles).find((t) => t.key === k)!;

describe("tileLabel", () => {
  it("names the state and the count", () => {
    expect(tileLabel(byKey("wireguard"), { state: "ok", count: 2 })).toBe("WireGuard VPN, on, 2 devices");
    expect(tileLabel(byKey("ssh"), { count: 1 })).toBe("SSH, 1 key");
    expect(tileLabel(byKey("notifications"), { state: "bad", count: 1 })).toBe("Notifications, failing, 1 channel");
  });

  it("falls back to a plain word for a state the tile does not name", () => {
    expect(tileLabel(byKey("console"), { state: "bad" })).toBe("Console, failed");
    expect(tileLabel(byKey("wireguard"), { state: "off" })).toBe("WireGuard VPN, off");
  });

  it("is the name alone with no badge", () => {
    expect(tileLabel(byKey("logs"), undefined)).toBe("Logs");
    expect(tileLabel(byKey("clock"), {})).toBe("Clock");
  });

  it("describes System Settings badges for apply errors and scheduled power", () => {
    expect(tileLabel(byKey("system"), { state: "bad" })).toBe("System Settings, not applied");
    expect(tileLabel(byKey("system"), { state: "info" })).toBe("System Settings, scheduled");
  });
});

describe("movedTile", () => {
  it("takes an old anchor to its own page", () => {
    expect(movedTile("#tailscale")).toBe("/settings/tailscale");
    expect(movedTile("#host")).toBe("/settings/system#host");
  });

  it("takes anything else to the index", () => {
    expect(movedTile("")).toBe("/settings");
    expect(movedTile("#nope")).toBe("/settings");
  });
});

describe("SECTIONS", () => {
  it("groups Settings into This host and Network with unique tile pages", () => {
    expect(SECTIONS.map((s) => s.title)).toEqual(["This host", "Network"]);
    const hostKeys = SECTIONS[0]!.tiles.map((t) => t.key);
    expect(hostKeys).toContain("system");
    expect(hostKeys).toContain("security");
    expect(hostKeys).toContain("assistant");
    expect(hostKeys).toContain("account");
    const tiles = SECTIONS.flatMap((s) => s.tiles);
    expect(new Set(tiles.map((t) => t.key)).size).toBe(tiles.length);
    for (const t of tiles) expect(t.href).toBe(`/settings/${t.key}`);
  });
});
