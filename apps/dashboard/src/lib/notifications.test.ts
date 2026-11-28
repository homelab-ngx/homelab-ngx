import { describe, expect, it } from "vitest";
import {
  attention,
  channelDraft,
  channelFilterSummary,
  channelInput,
  channelProblem,
  channelState,
  channelWhere,
  delivery,
  deliveryHasError,
  detail,
  entryHref,
  failingOf,
  kindTone,
  notificationHref,
  openOf,
  problemCount,
  type Channel,
  type Entry,
  type Snapshot,
} from "./notifications.ts";

const ntfy: Channel = {
  id: 1,
  name: "Phone",
  kind: "ntfy",
  enabled: true,
  server: "https://ntfy.sh",
  topic: "homelab",
  has_token: false,
  filter: { min_severity: "all", muted: [] },
  created_at: "",
};
const hook: Channel = {
  id: 2,
  name: "Hub",
  kind: "webhook",
  enabled: false,
  url: "https://192.0.2.1:8788/homelab/notify",
  cert_sha256: "a".repeat(64),
  has_token: true,
  filter: { min_severity: "alert", muted: ["backups", "services"] },
  created_at: "",
};

describe("channel form", () => {
  it("keeps the default server out of the field, keeps the token empty, and loads filter defaults", () => {
    expect(channelDraft(ntfy)).toMatchObject({ server: "", topic: "homelab", token: "", minSeverity: "all", muted: [] });
    expect(channelDraft(hook)).toMatchObject({ kind: "webhook", pin: "a".repeat(64), minSeverity: "alert", muted: ["backups", "services"] });
    expect(channelDraft(null)).toMatchObject({ minSeverity: "all", muted: [] });
  });
  it("sends only the kind's fields, normalized filter, and no empty token", () => {
    expect(channelInput({ ...channelDraft(null), name: " Phone ", topic: "t" })).toEqual({
      name: "Phone",
      kind: "ntfy",
      topic: "t",
      token: undefined,
      clear_token: undefined,
      server: undefined,
      filter: { min_severity: "all", muted: [] },
    });
    expect(channelInput({ ...channelDraft(hook), muted: ["services", "updates"] })).toMatchObject({
      kind: "webhook",
      url: hook.url,
      cert_sha256: hook.cert_sha256,
      token: undefined,
      filter: { min_severity: "alert", muted: ["updates", "services"] },
    });
  });
  it("says why a draft cannot be saved", () => {
    expect(channelProblem({ ...channelDraft(null), name: "" })).toMatch(/name/);
    expect(channelProblem({ ...channelDraft(null), name: "x", topic: "has space" })).toMatch(/topic/);
    expect(channelProblem({ ...channelDraft(hook), url: "http://x", pin: "a".repeat(64) })).toMatch(/https/);
    expect(channelProblem({ ...channelDraft(hook), pin: "abc" })).toMatch(/64/);
    expect(channelProblem(channelDraft(hook))).toBe("");
  });
});

describe("channel rows", () => {
  it("names where a channel delivers and summarizes active filters", () => {
    expect(channelWhere(ntfy)).toBe("homelab");
    expect(channelWhere({ ...ntfy, server: "https://ntfy.example.com" })).toBe("homelab on ntfy.example.com");
    expect(channelWhere(hook)).toBe(hook.url);
    expect(channelFilterSummary(ntfy)).toBe("");
    expect(channelFilterSummary(hook)).toBe("Alerts only · Backups, Services muted");
    expect(channelFilterSummary({ ...ntfy, filter: { min_severity: "all", muted: ["updates"] } })).toBe("Updates muted");
  });
  it("puts a test result before the stored state", () => {
    expect(channelState(ntfy, false, undefined)).toEqual({ tone: "ok", label: "on" });
    expect(channelState(ntfy, true, undefined).label).toBe("failing");
    expect(channelState(hook, false, undefined).label).toBe("off");
    expect(channelState(hook, false, { ok: false, error: "x" }).label).toBe("test failed");
  });
});

describe("the tray", () => {
  const snap: Snapshot = {
    history: [],
    latest_id: 12,
    read_up_to: 10,
    unread: 2,
    open: {
      "backup:4": { count: 1, opened_at: "2026-11-23T10:00:00Z", title: "Backup failed" },
      "disk:sdb:smart": { count: 2, opened_at: "2026-11-22T10:00:00Z", title: "sdb failing" },
    },
    failing_channels: { Phone: { since: "2026-11-23T09:00:00Z", error: "timeout" } },
  };

  it("counts open problems, failing channels, and unread entries for the bell", () => {
    expect(problemCount(snap)).toBe(3);
    expect(attention(snap)).toBe(5);
    expect(attention({ ...snap, open: {}, failing_channels: {}, unread: 2 })).toBe(2);
    expect(problemCount(undefined)).toBe(0);
    expect(attention(undefined)).toBe(0);
  });
  it("lists the oldest problem first", () => {
    expect(openOf(snap).map((p) => p.key)).toEqual(["disk:sdb:smart", "backup:4"]);
    expect(failingOf(snap).map((f) => f.name)).toEqual(["Phone"]);
  });

  const entry = (over: Partial<Entry>): Entry => ({ id: 1, at: "", body: "", deliveries: [], key: null, kind: "sent", severity: "info", title: "T", ...over });
  it("marks a row by what happened", () => {
    expect(kindTone(entry({ kind: "opened" }))).toBe("bad");
    expect(kindTone(entry({ kind: "reminded" }))).toBe("warn");
    expect(kindTone(entry({ kind: "resolved" }))).toBe("ok");
    expect(kindTone(entry({ kind: "sent", severity: "alert" }))).toBe("warn");
    expect(kindTone(entry({ kind: "sent" }))).toBe("idle");
  });
  it("reports delivery status and distinguishes filtered skips from failures", () => {
    expect(delivery(entry({}))).toBe("not sent");
    expect(deliveryHasError(entry({}))).toBe(false);

    const allFiltered = entry({
      deliveries: [{ channel: "Phone", ok: false, filtered: true, error: "", at: "" }],
    });
    expect(delivery(allFiltered)).toBe("not sent: filtered");
    expect(deliveryHasError(allFiltered)).toBe(false);

    const mixed = entry({
      deliveries: [
        { channel: "Phone", ok: false, error: "502", at: "" },
        { channel: "Hub", ok: false, filtered: true, error: "", at: "" },
        { channel: "Desk", ok: true, error: "", at: "" },
      ],
    });
    expect(delivery(mixed)).toBe("Phone failed, Hub filtered");
    expect(deliveryHasError(mixed)).toBe(true);

    expect(detail(entry({ title: "Disk", body: "Disk · 12 bad sectors\nmore" }))).toBe("12 bad sectors");
    expect(detail(entry({ title: "Disk", body: "Disk" }))).toBe("");
  });

  it("maps notification keys, stored paths, and titles to target pages", () => {
    expect(notificationHref("stack:media")).toBe("/stacks/media");
    expect(notificationHref("stack-install:home")).toBe("/stacks/home");
    expect(notificationHref("stack-update:home")).toBe("/stacks/home");
    expect(notificationHref("stack-rollback:home")).toBe("/stacks/home");
    expect(notificationHref("backup:4")).toBe("/backups/4");
    expect(notificationHref("backup-stopped:4")).toBe("/backups/4");
    expect(notificationHref("backup-stale:7")).toBe("/backups/7");
    expect(notificationHref("backup-coverage:home")).toBe("/backups");
    expect(notificationHref("disk:sdb:smart")).toBe("/storage");
    expect(notificationHref("disk-remove:sdc")).toBe("/storage");
    expect(notificationHref("image-release:0.4.3")).toBe("/settings/updates");
    expect(notificationHref("image-boot:check")).toBe("/settings/updates");
    expect(notificationHref("core-deploy:123")).toBe("/settings/updates");
    expect(notificationHref("host-rebuild:123")).toBe("/settings/updates");
    expect(notificationHref("host:power")).toBe("/settings/system");
    expect(notificationHref("churn:caddy.service")).toBe("/settings/logs?unit=caddy");
    expect(notificationHref("unit:homelab-health-audit.service")).toBe("/settings/logs?unit=homelab-health-audit");
    expect(notificationHref("daemon-crash")).toBe("/settings/logs?unit=homelab-daemon");
    expect(notificationHref("audit:smart")).toBe("/storage");
    expect(notificationHref("audit:cloudflare-tunnel")).toBe("/settings/tunnel");
    expect(notificationHref("audit:db-snapshot")).toBe("/backups");
    expect(notificationHref("security:audit:9")).toBe("/settings/security");
    expect(notificationHref(null, "Security audit ready")).toBe("/settings/security");
    expect(notificationHref(null, "Updated to 0.2.5")).toBe("/settings/updates");
    expect(notificationHref(null, "Not backed up: home")).toBe("/backups");
    expect(notificationHref(null, "Stack media did not start")).toBe("/stacks/media");
    expect(notificationHref(null, "Unrelated message")).toBeNull();

    expect(entryHref({ path: "/settings/system", key: null, title: "Host rebooting" })).toBe("/settings/system");
    expect(entryHref({ path: "/", key: "stack:media", title: "Stack media" })).toBe("/stacks/media");
    expect(entryHref({ path: "", key: null, title: "Unrelated message" })).toBeNull();
  });
});
