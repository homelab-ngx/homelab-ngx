import { describe, expect, it } from "vitest";
import {
  approvedStackTargets,
  changed,
  dashboardHost,
  ddnsName,
  inZone,
  isDashboardTarget,
  lanAddress,
  loginMinutesLeft,
  loginServerProblem,
  peerConfig,
  routePill,
  shortTunnelId,
  splitList,
  subnetChoices,
  subnetProblem,
  tailscaleExpiryDays,
  tailscaleNeedsPoll,
  tailscalePending,
  tailscalePendingMessage,
  tailscaleState,
  tunnelPill,
  type HostSettings,
  type TailscaleStatus,
} from "./host.ts";

const tsBase = (more: Partial<TailscaleStatus> = {}): TailscaleStatus => ({
  state: "running",
  ips: [],
  tags: [],
  health: [],
  routes: [],
  exit_node: "off",
  online_peers: 0,
  total_peers: 0,
  ...more,
});

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
    const wired = [{ name: "enp5s0", mac: "m", addresses: ["192.168.1.20"], subnets: ["192.168.1.0/24"] }];
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
  it("says Tailscale's state, pending approvals, expiry, and poll interval", () => {
    const now = Date.UTC(2026, 10, 27, 12, 0, 0);
    const soon = new Date(now + 5 * 86_400_000).toISOString();
    const later = new Date(now + 30 * 86_400_000).toISOString();
    const past = new Date(now - 3600_000).toISOString();

    expect(tailscaleState(tsBase({ key_expiry: later }), now)).toEqual({ tone: "ok", word: "connected" });
    expect(tailscaleState(tsBase({ key_expiry: soon }), now)).toEqual({ tone: "warn", word: "key expiring" });
    expect(tailscaleState(tsBase({ key_expiry: past }), now)).toEqual({ tone: "bad", word: "key expired" });
    expect(tailscaleState(tsBase({ state: "expired" }), now)).toEqual({ tone: "bad", word: "key expired" });
    expect(tailscaleState(tsBase({ state: "stopped" }), now)).toEqual({ tone: "warn", word: "stopped" });
    expect(tailscaleState(tsBase({ state: "needs-login" }), now)).toEqual({ tone: "warn", word: "logged out" });
    expect(tailscaleState(tsBase({ state: "needs-login", auth_url: "https://login.tailscale.com/a/1" }), now)).toEqual({
      tone: "info",
      word: "waiting for login",
    });
    expect(tailscaleState(tsBase({ state: "running", auth_url: "https://login.tailscale.com/a/2" }), now)).toEqual({
      tone: "info",
      word: "waiting for login",
    });
    expect(tailscaleState(tsBase({ health: ["relay issue"] }), now)).toEqual({ tone: "warn", word: "warning" });
    expect(
      tailscaleState(tsBase({ routes: [{ prefix: "192.168.1.0/24", approved: false }] }), now),
    ).toEqual({ tone: "warn", word: "awaiting approval" });
    expect(tailscaleState(undefined)).toEqual({ tone: "idle", word: "off" });

    expect(tailscaleExpiryDays(tsBase({ key_expiry: soon }), now)).toBe(5);
    expect(tailscalePending(tsBase({ routes: [{ prefix: "192.168.1.0/24", approved: false }], exit_node: "pending" }))).toEqual({
      routes: 1,
      exitNode: true,
      device: false,
      any: true,
    });
    expect(tailscalePendingMessage(tsBase({ routes: [{ prefix: "192.168.1.0/24", approved: false }], exit_node: "pending" }))).toBe(
      "1 subnet and the exit node wait for approval in the admin console.",
    );
    expect(tailscaleNeedsPoll(tsBase({ state: "needs-login", auth_url: "https://login.tailscale.com/a/1" }), now)).toBe(2000);
    expect(tailscaleNeedsPoll(tsBase({ state: "running", auth_url: "https://login.tailscale.com/a/2" }), now)).toBe(2000);
    expect(tailscaleNeedsPoll(tsBase({ routes: [{ prefix: "192.168.1.0/24", approved: false }] }), now)).toBe(30000);
    expect(tailscaleNeedsPoll(tsBase({ key_expiry: soon }), now)).toBe(30000);
    expect(tailscaleNeedsPoll(tsBase({ key_expiry: later }), now)).toBe(false);
  });

  it("picks unadded LAN/wired subnets and validates CIDRs and login servers", () => {
    const hs = {
      network: { lan: ["192.168.1.0/24"] },
      wired: [
        { name: "enp5s0", mac: "m", addresses: ["192.168.1.10"], subnets: ["192.168.1.0/24", "10.0.20.0/24"] },
      ],
    } as unknown as HostSettings;
    expect(subnetChoices(hs, [])).toEqual([
      { cidr: "192.168.1.0/24", label: "LAN" },
      { cidr: "10.0.20.0/24", label: "enp5s0" },
    ]);
    expect(subnetChoices(hs, ["192.168.1.0/24"])).toEqual([{ cidr: "10.0.20.0/24", label: "enp5s0" }]);
    expect(subnetProblem(["192.168.1.0/24", ""])).toBe("");
    expect(subnetProblem(["bad"])).toContain("192.168.1.0/24");
    expect(subnetProblem(["192.168.1.0/24", "192.168.1.0/24"])).toContain("duplicate");
    expect(loginServerProblem("")).toBe("");
    expect(loginServerProblem("https://headscale.example.net")).toBe("");
    expect(loginServerProblem("http://headscale.example.net")).toContain("https://");
  });
  it("sees a change", () => {
    expect(changed({ a: [1] }, { a: [1] })).toBe(false);
    expect(changed({ a: [1] }, { a: [2] })).toBe(true);
  });
  it("maps tunnel and route states to pills", () => {
    expect(tunnelPill(undefined, { state: "waiting" }, false, false)).toEqual({ tone: "info", word: "connecting" });
    expect(tunnelPill({ state: "connected", connections: 4, want: 4, colos: [], recent_errors: [], routes: [] }, undefined, true, true)).toEqual({ tone: "ok", word: "connected" });
    expect(tunnelPill({ state: "degraded", connections: 2, want: 4, colos: [], recent_errors: [], routes: [] }, undefined, true, true)).toEqual({ tone: "warn", word: "degraded" });
    expect(tunnelPill({ state: "down", connections: 0, want: 4, colos: [], recent_errors: [], routes: [] }, undefined, true, true)).toEqual({ tone: "bad", word: "down" });
    expect(tunnelPill(undefined, undefined, false, true)).toEqual({ tone: "idle", word: "off" });

    expect(routePill({ hostname: "a", target: "t", public: false, dns: "failed", protection: "unreachable" }, true)).toEqual({ tone: "bad", word: "dns failed" });
    expect(routePill({ hostname: "a", target: "t", public: false, dns: "added", protection: "open" }, true)).toEqual({ tone: "warn", word: "open" });
    expect(routePill({ hostname: "a", target: "t", public: true, dns: "added", protection: "public" }, true)).toEqual({ tone: "ok", word: "public" });
    expect(routePill({ hostname: "a", target: "t", public: false, dns: "added", protection: "protected", no_target: true }, true)).toEqual({ tone: "warn", word: "no target" });
    expect(routePill({ hostname: "a", target: "t", public: false, dns: "added", protection: "protected" }, true)).toEqual({ tone: "ok", word: "protected" });
    expect(routePill({ hostname: "a", target: "t", public: false, dns: "added", protection: "not-served" }, true)).toEqual({ tone: "idle", word: "not served" });
  });
  it("checks tunnel zones, dashboard targets, and approved stack routes", () => {
    expect(shortTunnelId("6f1c1234-5678-90ab-cdef-12345678e2a0")).toBe("6f1c…e2a0");
    expect(inZone("photos.example.net", "example.net")).toBe(true);
    expect(inZone("example.net", "example.net")).toBe(true);
    expect(inZone("photos.other.org", "example.net")).toBe(false);
    expect(isDashboardTarget("homelab", { domain: "home.example", dashboard_label: "" })).toBe(true);
    expect(isDashboardTarget("homelab.home.example", { domain: "home.example", dashboard_label: "" })).toBe(true);
    expect(isDashboardTarget("photos", { domain: "home.example", dashboard_label: "" })).toBe(false);
    expect(loginMinutesLeft("2026-11-27T08:10:00Z", Date.parse("2026-11-27T08:01:30Z"))).toBe(9);
    const opts = approvedStackTargets(
      [{ name: "immich", label: "Photos", approved: { manifest: { routes: [{ host: "photos.home.example", port: 2283 }] } } }] as never,
      { domain: "home.example", dashboard_label: "homelab" },
    );
    expect(opts).toEqual([{ stack: "immich", stackTitle: "Photos", host: "photos.home.example", target: "photos" }]);
  });
});
