import { describe, expect, it } from "vitest";
import { newHost, powerProfile } from "../demo/host.ts";
import { appliedState, blockerRow, share, tunableRow, watts, type PowerProfileStatus } from "./power-profile.ts";

function status(over: Partial<PowerProfileStatus> = {}): PowerProfileStatus {
  const h = newHost(() => 0);
  return { ...powerProfile(h), ...over };
}

describe("power profile", () => {
  it("formats watts and shares", () => {
    expect(watts(1.04)).toBe("1.0 W");
    expect(watts(14.6)).toBe("15 W");
    expect(watts(null)).toBe("");
    expect(share(0.004)).toBe("<1%");
    expect(share(0)).toBe("0%");
    expect(share(0.786)).toBe("79%");
  });

  it("says what the profile did", () => {
    expect(appliedState(status())).toEqual({ tone: "ok", word: "applied" });
    expect(appliedState(status({ read_only: true })).word).toBe("read only");
    expect(appliedState(status({ tunables: [] })).word).toBe("applying");
    const refused = status();
    refused.tunables[2] = { ...refused.tunables[2]!, state: "refused", refused: 1, files: 1, detail: "not allowed by firmware" };
    expect(appliedState(refused)).toEqual({ tone: "warn", word: "partial" });
    expect(tunableRow(refused.tunables[2]!)).toEqual({ tone: "bad", word: "refused", value: "1 of 1 refused: not allowed by firmware" });
  });

  it("names a tunable's value", () => {
    const t = { name: "epp", files: 8, refused: 0, value: "", detail: "set by the performance governor", state: "boot" } as const;
    expect(tunableRow(t)).toEqual({ tone: "idle", word: "default", value: "set by the performance governor" });
    expect(tunableRow({ ...t, detail: "" }).value).toBe("as booted");
    expect(tunableRow({ ...t, state: "unsupported", files: 0 }).word).toBe("none");
    expect(tunableRow({ ...t, name: "aspm_links", value: "1", state: "set" }).value).toBe("on");
    expect(tunableRow({ ...t, name: "nmi_watchdog", value: "0", state: "set" }).value).toBe("off");
  });

  it("names a blocker and its fix", () => {
    const [nic, ups] = status().blockers;
    expect(blockerRow(nic!)).toEqual({ name: "r8169", detail: "0000:05:00.0 · 10ec:8125 · PCIe link L1 off · Power saver turns it off" });
    expect(blockerRow(ups!).name).toBe("Back-UPS ES 700");
  });

});
