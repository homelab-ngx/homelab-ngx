import { describe, expect, it } from "vitest";
import { changed, dashboardHost, ddnsName, lanAddress, peerConfig, splitList, tailscaleState } from "./host.ts";

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
  it("finds the host's LAN address for the router's port forward", () => {
    const wired = [{ name: "enp5s0", mac: "m", addresses: ["192.168.1.20"] }];
    expect(lanAddress({ network: { lan: [], ipv4: { mode: "static", address: "192.168.1.30/24" } }, wired })).toBe("192.168.1.30");
    expect(lanAddress({ network: { lan: [], ipv4: { mode: "dhcp" } }, wired })).toBe("192.168.1.20");
    expect(lanAddress({ network: { lan: [] }, wired: [] })).toBe("");
  });
  it("names the dynamic DNS record", () => {
    const domain = (d: string, p: "" | "cloudflare" | "duckdns") => ({ domain: { domain: d, dns_provider: p, dashboard_label: "", acme_email: "", dns_credentials_set: true } });
    expect(ddnsName(domain("home.example.com", "cloudflare"), "")).toBe("vpn.home.example.com");
    expect(ddnsName(domain("home.example.com", "cloudflare"), "home")).toBe("home.home.example.com");
    expect(ddnsName(domain("myhome.duckdns.org", "duckdns"), "vpn")).toBe("myhome.duckdns.org");
    expect(ddnsName(domain("", "cloudflare"), "vpn")).toBe("");
  });
  it("says Tailscale's state", () => {
    expect(tailscaleState({ state: "running", ips: [] })).toEqual({ tone: "ok", word: "connected" });
    expect(tailscaleState({ state: "needs-login", ips: [] }).tone).toBe("warn");
    expect(tailscaleState(undefined).word).toBe("off");
  });
  it("sees a change", () => {
    expect(changed({ a: [1] }, { a: [1] })).toBe(false);
    expect(changed({ a: [1] }, { a: [2] })).toBe(true);
  });
});
