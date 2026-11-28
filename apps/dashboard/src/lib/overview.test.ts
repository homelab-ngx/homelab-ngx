import { describe, expect, it } from "vitest";
import type { Schema, ServiceInfo, Stack } from "./api/client.ts";
import type { Backup } from "./backups.ts";
import type { HostSettings } from "./host.ts";
import { attentionItems, setupChecklist } from "./overview.ts";

const svc = (unit_name: string, active_state: string, extra: Partial<ServiceInfo> = {}): ServiceInfo =>
  ({ name: unit_name, unit_name, active_state, sub_state: "", type: "Native", description: "", masked: false, started_at: null, ...extra }) as ServiceInfo;

const stack = (name: string, update_state: Stack["update_state"], generation = 1): Stack =>
  ({ name, generation, update_state } as unknown as Stack);

const disk = (name: string, state: string, health?: { passed: boolean; bad_sectors: number }): Schema<"Disk"> =>
  ({
    name,
    path: `/dev/${name}`,
    state,
    ...(health ? { health: { passed: health.passed, bad_sectors: health.bad_sectors, temperature_c: 30, power_on_hours: 10, checked_at: "" } } : {}),
  }) as Schema<"Disk">;

describe("attentionItems", () => {
  it("stays empty when everything is healthy", () => {
    expect(
      attentionItems({
        services: [svc("media-plex.service", "active", { stack: "media" }), svc("media-old.service", "failed", { stack: "media", masked: true })],
        coreServices: [svc("homelab-daemon.service", "active")],
        notifications: { open: {}, failing_channels: {}, history: [], latest_id: 0, read_up_to: 0, unread: 0 },
        stacks: [stack("media", "up_to_date")],
        settingsStatus: { tiles: { domain: { state: "ok" } } },
        storage: { pools: [{ name: "tank", disks: [disk("sda", "rw", { passed: true, bad_sectors: 0 })] } as Schema<"Pool">], unassigned: [] } as unknown as Schema<"StorageStatus">,
        coverage: { stacks: [{ stack: "media", prepare: "", folders: [{ key: "d", label: "Data", host_path: "/pool/data", covered_by: ["daily"], excludes: [], problem: "" }] }], uncovered: 0 },
      }),
    ).toEqual([]);
  });

  it("collects every category, links each to its resolution page, and orders worst first", () => {
    const items = attentionItems({
      services: [svc("media-plex.service", "failed", { stack: "media" })],
      coreServices: [svc("caddy.service", "failed")],
      notifications: {
        open: { "disk:sdb:smart": { count: 1, opened_at: "2026-01-01T00:00:00Z", title: "Disk" } },
        failing_channels: { ntfy: { error: "502", since: "2026-01-01T00:00:00Z" } },
        history: [],
        latest_id: 0,
        read_up_to: 0,
        unread: 0,
      },
      stacks: [stack("media", "commits_available"), stack("home", "needs_approval")],
      settingsStatus: { tiles: { updates: { state: "info" } } },
      storage: {
        pools: [{ name: "tank", disks: [disk("sdb", "rw", { passed: true, bad_sectors: 4 })] } as Schema<"Pool">],
        unassigned: [disk("sdc", "", { passed: false, bad_sectors: 0 })],
      } as unknown as Schema<"StorageStatus">,
      coverage: { stacks: [{ stack: "home", prepare: "", folders: [{ key: "cfg", label: "Config", host_path: "/pool/cfg", covered_by: [], excludes: [], problem: "" }] }], uncovered: 1 },
    });

    expect(items).toEqual([
      { id: "failed-services", tone: "bad", label: "1 failed service", href: "/stacks/media" },
      { id: "failed-host-services", tone: "bad", label: "1 failed host service", href: "/settings/logs?unit=caddy" },
      { id: "failing-channels", tone: "bad", label: "1 failing channel", href: "/settings/notifications" },
      { id: "open-problems", tone: "bad", label: "1 open problem", action: "notifications" },
      { id: "degraded-disks", tone: "bad", label: "2 degraded disks", href: "/storage" },
      { id: "stack-updates", tone: "warn", label: "2 stack updates", href: "/stacks" },
      { id: "uncovered-backups", tone: "warn", label: "1 not backed up", href: "/backups" },
      { id: "core-update", tone: "info", label: "Core update ready", href: "/settings/updates" },
    ]);
  });

  it("reports a fallback core update as bad", () => {
    expect(attentionItems({ settingsStatus: { tiles: { updates: { state: "bad" } } } })).toEqual([
      { id: "core-update-failed", tone: "bad", label: "Update fell back", href: "/settings/updates" },
    ]);
  });
});

const emptyHost = (overrides: Partial<HostSettings> = {}): HostSettings =>
  ({
    domain: { domain: "", dashboard_label: "homelab", dns_provider: "", dns_credentials_set: false, acme_email: "" },
    wireguard: { enabled: false, peers: [] },
    tunnel: { enabled: false, credentials_set: false, dns_managed: false, routes: [] },
    tailscale: { enabled: false },
    ...overrides,
  }) as unknown as HostSettings;

const emptyStorage = (pools = 0): Schema<"StorageStatus"> =>
  ({ pools: Array.from({ length: pools }, (_, i) => ({ name: `p${i}` })), unassigned: [] }) as unknown as Schema<"StorageStatus">;

describe("setupChecklist", () => {
  it("stays empty while any required query is still loading", () => {
    expect(setupChecklist({ storage: emptyStorage(0), host: emptyHost(), stacks: [] })).toEqual([]);
  });

  it("shows four incomplete steps on a fresh host", () => {
    const steps = setupChecklist({
      storage: emptyStorage(0),
      host: emptyHost(),
      stacks: [],
      backups: [],
    });
    expect(steps).toEqual([
      { id: "storage", label: "Create a storage pool", href: "/storage", done: false },
      { id: "remote", label: "Set up a domain or VPN", href: "/settings/domain", done: false },
      { id: "stack", label: "Add a stack", href: "/stacks/add", done: false },
      { id: "backup", label: "Configure a backup", href: "/backups", done: false },
    ]);
  });

  it("tracks partial completion including VPN alternatives", () => {
    const steps = setupChecklist({
      storage: emptyStorage(1),
      host: emptyHost({ tailscale: { enabled: true } as HostSettings["tailscale"] }),
      stacks: [stack("media", "up_to_date")],
      backups: [],
    });
    expect(steps.map((s) => `${s.id}:${s.done}`)).toEqual(["storage:true", "remote:true", "stack:true", "backup:false"]);
  });

  it("returns empty once all four setup steps are complete", () => {
    expect(
      setupChecklist({
        storage: emptyStorage(1),
        host: emptyHost({ domain: { domain: "home.example", dashboard_label: "homelab", dns_provider: "cloudflare", dns_credentials_set: true, acme_email: "" } }),
        stacks: [stack("media", "up_to_date")],
        backups: [{ id: 1, name: "daily" } as unknown as Backup],
      }),
    ).toEqual([]);
  });
});
