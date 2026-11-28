import type { Schema } from "#lib/api/client.ts";
import type { NotificationStreamSource } from "#lib/live-notifications.svelte.ts";
import type { EventCategory } from "#lib/notifications.ts";
import type { Host } from "./host.ts";

const MIN = 60_000;
const DAY = 24 * 60 * MIN;
const CATEGORIES: EventCategory[] = ["updates", "stacks", "backups", "services", "security"];

export interface DemoNotificationsState {
  channels: Schema<"Channel">[];
  entries: Schema<"NotificationLogEntry">[];
  open: Record<string, Schema<"OpenProblem">>;
  readUpTo: number;
  listeners: Set<(type: string, data: unknown) => void>;
}

const iso = (ms: number) => new Date(ms).toISOString();

export function seedNotifications(t: number): DemoNotificationsState {
  return {
    channels: [
      {
        id: 1,
        name: "Phone",
        kind: "ntfy",
        enabled: true,
        has_token: true,
        server: "https://ntfy.sh",
        topic: "homelab-demo",
        filter: { min_severity: "all", muted: [] },
        created_at: iso(t - 100 * DAY),
      },
      {
        id: 2,
        name: "Home Assistant",
        kind: "webhook",
        enabled: true,
        has_token: false,
        url: "https://home.home.example/api/webhook/homelab",
        filter: { min_severity: "alert", muted: ["backups"] },
        created_at: iso(t - 40 * DAY),
      },
    ],
    open: {
      "disk:sdb:smart": { title: "Disk sdb reports 8 bad sectors", count: 3, opened_at: iso(t - 2 * DAY) },
      "backup:4": { title: "Backup Media, weekly failed", count: 1, opened_at: iso(t - 3 * DAY) },
    },
    entries: [
      {
        id: 4,
        at: iso(t - 20 * MIN),
        key: "image-release:0.2.6",
        kind: "sent",
        event: "core.available",
        path: "/settings/updates",
        severity: "info",
        title: "Version 0.2.6 is ready to install",
        body: "It is downloaded and verified. Install it from Settings when it suits you; it boots at the next reboot.",
        deliveries: [
          { at: iso(t - 20 * MIN), channel: "Phone", ok: true, error: "" },
          { at: iso(t - 20 * MIN), channel: "Home Assistant", ok: false, filtered: true, error: "" },
        ],
      },
      {
        id: 3,
        at: iso(t - 2 * DAY),
        key: "disk:sdb:smart",
        kind: "opened",
        event: "doctor.check",
        path: "/storage",
        severity: "alert",
        title: "Disk sdb reports 8 bad sectors",
        body: "ST8000VN004 in pool tank. The pool keeps two copies of everything.",
        deliveries: [{ at: iso(t - 2 * DAY), channel: "Phone", ok: true, error: "" }],
      },
      {
        id: 2,
        at: iso(t - 3 * DAY),
        key: "backup:4",
        kind: "opened",
        event: "backup.failed",
        path: "/backups/4",
        severity: "alert",
        title: "Backup Media, weekly failed",
        body: "the destination did not answer: dial tcp: i/o timeout",
        deliveries: [{ at: iso(t - 3 * DAY), channel: "Phone", ok: true, error: "" }],
      },
      {
        id: 1,
        at: iso(t - 6 * DAY),
        key: "core-deploy:12",
        kind: "sent",
        event: "core.update",
        path: "/settings/updates",
        severity: "info",
        title: "Updated to 0.2.5",
        body: "The host runs 0.2.5.",
        deliveries: [{ at: iso(t - 6 * DAY), channel: "Phone", ok: true, error: "" }],
      },
    ],
    readUpTo: 3,
    listeners: new Set(),
  };
}

function unreadCount(n: DemoNotificationsState): number {
  let count = 0;
  for (const e of n.entries) {
    if (e.id <= n.readUpTo) continue;
    if ((e.kind === "opened" || e.kind === "reminded") && e.key && n.open[e.key]) continue;
    count++;
  }
  return count;
}

export function snapshotOf(h: Host): Schema<"NotificationsSnapshot"> {
  const n = h.notifications;
  const latestId = n.entries.reduce((max, e) => Math.max(max, e.id), 0);
  const history: Schema<"NotificationHistoryEntry">[] = [];
  for (let i = n.entries.length - 1; i >= 0; i--) {
    const e = n.entries[i]!;
    for (const d of e.deliveries) {
      if (d.filtered) continue;
      history.push({
        at: d.at,
        channel: d.channel,
        severity: e.severity,
        title: e.title,
        ok: d.ok,
        ...(d.error ? { error: d.error } : {}),
      });
    }
  }
  return {
    failing_channels: {},
    open: n.open,
    history,
    read_up_to: n.readUpTo,
    latest_id: latestId,
    unread: unreadCount(n),
  };
}

function emitSnapshot(h: Host) {
  const snap = snapshotOf(h);
  for (const fn of h.notifications.listeners) fn("snapshot", snap);
}

export function markNotificationsRead(h: Host, upTo: number): Schema<"MarkReadResponse"> {
  const n = h.notifications;
  const latest = n.entries.reduce((max, e) => Math.max(max, e.id), 0);
  const target = upTo <= 0 || upTo > latest ? latest : upTo;
  if (target > n.readUpTo) n.readUpTo = target;
  emitSnapshot(h);
  return { read_up_to: n.readUpTo, unread: unreadCount(n) };
}

export function listNotificationLog(h: Host, before = 0, limit = 50): Schema<"NotificationLogEntry">[] {
  const items = before > 0 ? h.notifications.entries.filter((e) => e.id < before) : h.notifications.entries;
  return items.slice(0, limit);
}

export function listNotificationChannels(h: Host): Schema<"Channel">[] {
  return h.notifications.channels;
}

function parseFilter(raw: unknown, fallback?: Schema<"ChannelFilter">): Schema<"ChannelFilter"> {
  if (!raw || typeof raw !== "object") {
    return fallback ?? { min_severity: "all", muted: [] };
  }
  const f = raw as { min_severity?: unknown; muted?: unknown };
  const minSev = f.min_severity === "alert" ? "alert" : "all";
  const mutedList = Array.isArray(f.muted) ? f.muted.filter((x): x is EventCategory => CATEGORIES.includes(x as EventCategory)) : [];
  return {
    min_severity: minSev,
    muted: CATEGORIES.filter((c) => mutedList.includes(c)),
  };
}

export function createNotificationChannel(h: Host, body: Record<string, unknown>): Schema<"Channel"> {
  const id = h.notifications.channels.reduce((m, c) => Math.max(m, c.id), 0) + 1;
  const kind = body.kind === "webhook" ? "webhook" : "ntfy";
  const ch: Schema<"Channel"> = {
    id,
    name: String(body.name ?? "Channel").trim() || "Channel",
    kind,
    enabled: body.enabled === undefined ? true : Boolean(body.enabled),
    has_token: Boolean(body.token),
    ...(kind === "ntfy"
      ? { server: String(body.server ?? "https://ntfy.sh") || "https://ntfy.sh", topic: String(body.topic ?? "") }
      : { url: String(body.url ?? ""), ...(body.cert_sha256 ? { cert_sha256: String(body.cert_sha256) } : {}) }),
    filter: parseFilter(body.filter),
    created_at: iso(h.now()),
  };
  h.notifications.channels = [...h.notifications.channels, ch].sort((a, b) => a.name.localeCompare(b.name));
  emitSnapshot(h);
  return ch;
}

export function updateNotificationChannel(h: Host, id: number, body: Record<string, unknown>): Schema<"Channel"> | null {
  const idx = h.notifications.channels.findIndex((c) => c.id === id);
  if (idx < 0) return null;
  const cur = h.notifications.channels[idx]!;
  const hasToken = body.clear_token ? false : body.token ? true : cur.has_token;
  const updated: Schema<"Channel"> = {
    ...cur,
    name: String(body.name ?? cur.name).trim() || cur.name,
    enabled: body.enabled === undefined ? cur.enabled : Boolean(body.enabled),
    has_token: hasToken,
    ...(cur.kind === "ntfy"
      ? { server: String(body.server ?? cur.server ?? "https://ntfy.sh") || "https://ntfy.sh", topic: String(body.topic ?? cur.topic ?? "") }
      : {
          url: String(body.url ?? cur.url ?? ""),
          cert_sha256: body.cert_sha256 === undefined ? cur.cert_sha256 : String(body.cert_sha256 || "") || undefined,
        }),
    filter: parseFilter(body.filter, cur.filter),
  };
  h.notifications.channels = h.notifications.channels.map((c) => (c.id === id ? updated : c));
  emitSnapshot(h);
  return updated;
}

export function deleteNotificationChannel(h: Host, id: number): boolean {
  const before = h.notifications.channels.length;
  h.notifications.channels = h.notifications.channels.filter((c) => c.id !== id);
  if (h.notifications.channels.length === before) return false;
  emitSnapshot(h);
  return true;
}

/** Demo notification SSE source pushing the initial snapshot and live updates. */
export function notificationsSource(h: Host): NotificationStreamSource {
  const handlers = new Map<string, (e: Event) => void>();
  const dispatch = (type: string, data: unknown) => handlers.get(type)?.(new MessageEvent(type, { data: JSON.stringify(data) }));
  h.notifications.listeners.add(dispatch);
  const first = setTimeout(() => dispatch("snapshot", snapshotOf(h)), 0);
  return {
    addEventListener: (type, fn) => void handlers.set(type, fn),
    close: () => {
      clearTimeout(first);
      h.notifications.listeners.delete(dispatch);
    },
    onerror: null,
  };
}
