import { describe, expect, it } from "vitest";
import { changed, dashboardHost, peerConfig, splitList } from "./host.ts";

describe("host settings", () => {
  it("reads a typed list", () => {
    expect(splitList(" 192.168.1.0/24,10.0.0.0/8\n 172.16.0.0/12 ")).toEqual(["192.168.1.0/24", "10.0.0.0/8", "172.16.0.0/12"]);
    expect(splitList("  ")).toEqual([]);
  });
  it("names the dashboard's address", () => {
    expect(dashboardHost({ domain: "home.example.com", dashboard_label: "" })).toBe("homelab.home.example.com");
    expect(dashboardHost({ domain: "home.example.com", dashboard_label: "lab" })).toBe("lab.home.example.com");
    expect(dashboardHost({ domain: "", dashboard_label: "lab" })).toBe("");
  });
  it("writes a peer's config without a private key", () => {
    const c = peerConfig({ public_key: "SERVER=", endpoint: "vpn.example.com:51820", address: "10.10.0.1/24" }, { name: "phone", public_key: "P=", address: "10.10.0.2/32" });
    expect(c).toContain("Address = 10.10.0.2/32");
    expect(c).toContain("PublicKey = SERVER=");
    expect(c).toContain("AllowedIPs = 10.10.0.0/24");
  });
  it("sees a change", () => {
    expect(changed({ a: [1] }, { a: [1] })).toBe(false);
    expect(changed({ a: [1] }, { a: [2] })).toBe(true);
  });
});
