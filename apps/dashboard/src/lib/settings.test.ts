import { describe, expect, it } from "vitest";
import type { SecurityAuditItem } from "./api/client.ts";
import type { HostSettings } from "./host.ts";
import type { Channel, Snapshot } from "./notifications.ts";
import {
  appliedStatus,
  auditStatus,
  clockStatus,
  consoleLine,
  domainLine,
  firewallLine,
  hostLine,
  lanLine,
  notificationsStatus,
  sshLine,
  tailscaleStatus,
  tunnelStatus,
  vpnStatus,
} from "./settings.ts";

function settings(over: Partial<HostSettings> = {}): HostSettings {
  return {
    applied: { errors: [] },
    console: {},
    ddns: { enabled: false },
    domain: { acme_email: "", dashboard_label: "", dns_credentials_set: false, dns_provider: "", domain: "" },
    firewall: { openings: [] },
    host: {},
    managed: "image",
    network: { lan: [] },
    tailscale: { enabled: false },
    time: {},
    time_sync: { synced: true },
    tunnel: { credentials_set: false, dns_managed: false, enabled: false, routes: [] },
    wired: [],
    wireguard: { enabled: false, peers: [] },
    ...over,
  } as HostSettings;
}

describe("this host's tiles", () => {
  it("names the host and its zone, with defaults when unset", () => {
    expect(hostLine(settings()).text).toBe("homelab · UTC");
    expect(hostLine(settings({ host: { hostname: "nas", timezone: "Europe/Berlin" } })).text).toBe("nas · Europe/Berlin");
  });
  it("warns when the clock is not synced", () => {
    expect(clockStatus(settings())).toEqual({ tone: "ok", text: "synced" });
    expect(clockStatus(settings({ time_sync: { synced: false } })).tone).toBe("warn");
  });
  it("names the keyboard layout from the host's list", () => {
    const layouts = [{ name: "se", description: "Swedish" }];
    expect(consoleLine(settings(), layouts).text).toBe("English (US)");
    expect(consoleLine(settings({ console: { keyboard: "se" } }), layouts).text).toBe("Swedish");
    expect(consoleLine(settings({ console: { keyboard: "de" } }), undefined).text).toBe("de");
  });
  it("puts a failing channel before the count", () => {
    const on = { enabled: true } as Channel;
    const failing = { failing_channels: { Phone: { since: "", error: "x" } }, open: {}, history: [] } as Snapshot;
    expect(notificationsStatus(undefined, undefined)).toBeUndefined();
    expect(notificationsStatus([], undefined)).toEqual({ tone: "idle", text: "no channels" });
    expect(notificationsStatus([on, on], undefined)).toEqual({ tone: "ok", text: "on", count: 2 });
    expect(notificationsStatus([on], failing)).toEqual({ tone: "bad", text: "failing", count: 1 });
  });
});

describe("network tiles", () => {
  it("says whether the settings are in effect", () => {
    expect(appliedStatus(settings()).text).toBe("applied");
    expect(appliedStatus(settings({ applied: { errors: ["x"] } })).text).toBe("not applied");
    expect(appliedStatus(settings({ managed: "nixos" })).text).toBe("nixos");
  });
  it("shows what is set, or that nothing is", () => {
    expect(domainLine(settings()).text).toBe("not set");
    expect(lanLine(settings({ network: { lan: ["192.168.1.0/24"] } })).text).toBe("192.168.1.0/24");
    expect(firewallLine(settings()).text).toBe("no openings");
    expect(sshLine(settings({ host: { ssh_keys: ["a", "b"] } }))).toEqual({ text: "keys", count: 2 });
    expect(firewallLine(settings({ firewall: { openings: [{}] as HostSettings["firewall"]["openings"] } })).count).toBe(1);
  });
  it("reads Tailscale's state only when it is on", () => {
    expect(tailscaleStatus(settings(), { state: "running", ips: [] })).toEqual({ tone: "idle", text: "off" });
    expect(tailscaleStatus(settings({ tailscale: { enabled: true } }), { state: "running", ips: [] })).toEqual({ tone: "ok", text: "connected" });
  });
  it("counts devices and routes only when on", () => {
    const peers = [{}, {}, {}] as HostSettings["wireguard"]["peers"];
    expect(vpnStatus(settings({ wireguard: { enabled: true, peers } }))).toEqual({ tone: "ok", text: "on", count: 3 });
    expect(vpnStatus(settings({ wireguard: { enabled: false, peers } })).text).toBe("off");
    expect(tunnelStatus(settings({ tunnel: { enabled: true, credentials_set: false, dns_managed: false, routes: [] } })).text).toBe("off");
  });
});

describe("the audit tile", () => {
  it("reads the newest audit's posture, else its result", () => {
    expect(auditStatus([])).toEqual({ tone: "idle", text: "never" });
    expect(auditStatus([{ posture: "fair", result: "done" } as SecurityAuditItem])).toEqual({ tone: "info", text: "fair" });
    expect(auditStatus([{ result: "running" } as SecurityAuditItem])?.text).toBe("running");
  });
});
