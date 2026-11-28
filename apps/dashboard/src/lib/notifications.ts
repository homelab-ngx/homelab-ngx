/** Notification channels: the pure logic behind the Channels section and its form. */
import type { Schema } from "./api/client.ts";
import type { Tone } from "./components/Pill.svelte";

export type Channel = Schema<"Channel">;
export type ChannelInput = Schema<"ChannelInput">;
export type ChannelKind = Channel["kind"];
export type ChannelFilter = Schema<"ChannelFilter">;
export type ChannelMinSeverity = NonNullable<ChannelFilter["min_severity"]>;
export type EventCategory = NonNullable<ChannelFilter["muted"]>[number];

export const EVENT_CATEGORIES: { id: EventCategory; label: string }[] = [
  { id: "updates", label: "Updates" },
  { id: "stacks", label: "Stacks" },
  { id: "backups", label: "Backups" },
  { id: "services", label: "Services" },
  { id: "security", label: "Security" },
];

const CATEGORY_LABELS: Record<EventCategory, string> = {
  updates: "Updates",
  stacks: "Stacks",
  backups: "Backups",
  services: "Services",
  security: "Security",
};

export const channelKeys = {
  list: ["daemon", "notifications", "channels"] as const,
};

export const DEFAULT_NTFY = "https://ntfy.sh";

export const KIND_LABEL: Record<ChannelKind, string> = { ntfy: "ntfy", webhook: "Webhook" };

/** The form's editable copy of a channel. */
export interface ChannelDraft {
  kind: ChannelKind;
  name: string;
  server: string;
  topic: string;
  url: string;
  pin: string;
  token: string;
  clearToken: boolean;
  minSeverity: ChannelMinSeverity;
  muted: EventCategory[];
}

export function channelDraft(c: Channel | null): ChannelDraft {
  return {
    kind: c?.kind ?? "ntfy",
    name: c?.name ?? "",
    server: c?.server && c.server !== DEFAULT_NTFY ? c.server : "",
    topic: c?.topic ?? "",
    url: c?.url ?? "",
    pin: c?.cert_sha256 ?? "",
    token: "",
    clearToken: false,
    minSeverity: c?.filter?.min_severity ?? "all",
    muted: [...(c?.filter?.muted ?? [])],
  };
}

/** The request body for d; enabled stays as it is. */
export function channelInput(d: ChannelDraft): ChannelInput {
  const filter: ChannelFilter = {
    min_severity: d.minSeverity,
    muted: EVENT_CATEGORIES.map((c) => c.id).filter((id) => d.muted.includes(id)),
  };
  const base = {
    name: d.name.trim(),
    kind: d.kind,
    token: d.token.trim() || undefined,
    clear_token: d.clearToken || undefined,
    filter,
  };
  return d.kind === "ntfy"
    ? { ...base, server: d.server.trim() || undefined, topic: d.topic.trim() }
    : { ...base, url: d.url.trim(), cert_sha256: d.pin.trim() || undefined };
}

/** Human summary of a channel's filter when it narrows external delivery, else "". */
export function channelFilterSummary(c: Channel): string {
  const parts: string[] = [];
  if (c.filter?.min_severity === "alert") parts.push("Alerts only");
  const muted = (c.filter?.muted ?? []).map((id) => CATEGORY_LABELS[id] ?? id);
  if (muted.length > 0) parts.push(`${muted.join(", ")} muted`);
  return parts.join(" · ");
}

const isHttp = (s: string) => /^https?:\/\/[^\s/]+/.test(s.trim());

/** Why the form cannot be saved yet, or "". */
export function channelProblem(d: ChannelDraft): string {
  if (!d.name.trim()) return "Give the channel a name.";
  if (d.kind === "ntfy") {
    if (d.server.trim() && !isHttp(d.server)) return "The server is an http or https address.";
    if (!/^[-_A-Za-z0-9]{1,64}$/.test(d.topic.trim())) return "The topic is letters, digits, - and _.";
    return "";
  }
  if (!isHttp(d.url)) return "The address is an http or https address.";
  if (d.pin.trim()) {
    if (!/^[0-9a-fA-F]{64}$/.test(d.pin.trim())) return "The certificate pin is 64 hex characters.";
    if (!d.url.trim().startsWith("https://")) return "A certificate pin needs an https address.";
  }
  return "";
}

/** Where a channel delivers, for its row. */
export function channelWhere(c: Channel): string {
  if (c.kind === "ntfy") return c.server && c.server !== DEFAULT_NTFY ? `${c.topic} on ${c.server.replace(/^https?:\/\//, "")}` : `${c.topic ?? ""}`;
  return c.url ?? "";
}

/** A channel's row state: the last test, else whether it is off or failing. */
export function channelState(c: Channel, failing: boolean, test: Schema<"ChannelTest"> | "running" | undefined): { tone: Tone; label: string } {
  if (test === "running") return { tone: "info", label: "testing" };
  if (test) return test.ok ? { tone: "ok", label: "test sent" } : { tone: "bad", label: "test failed" };
  if (!c.enabled) return { tone: "idle", label: "off" };
  return failing ? { tone: "bad", label: "failing" } : { tone: "ok", label: "on" };
}

/** The snapshot: open problems and failing channels. The header's bell reads it on every page. */
export type Snapshot = Schema<"NotificationsSnapshot">;
export type Entry = Schema<"NotificationLogEntry">;

export const notificationKeys = {
  snapshot: ["daemon", "notifications"] as const,
  log: ["daemon", "notifications", "log"] as const,
};

/** The history's page size. */
export const LOG_PAGE = 50;

/** The channels that fail to deliver, longest failing first. */
export function failingOf(s: Snapshot | undefined) {
  return Object.entries(s?.failing_channels ?? {})
    .map(([name, f]) => ({ name, ...f }))
    .sort((a, b) => a.since.localeCompare(b.since));
}

/** The open problems, oldest first. */
export function openOf(s: Snapshot | undefined) {
  return Object.entries(s?.open ?? {})
    .map(([key, p]) => ({ key, ...p }))
    .sort((a, b) => a.opened_at.localeCompare(b.opened_at));
}

/** Open problems and failing channels, without unread info noise. */
export function problemCount(s: Snapshot | undefined): number {
  return Object.keys(s?.open ?? {}).length + Object.keys(s?.failing_channels ?? {}).length;
}

/** What the bell counts: open problems, failing channels, and unread history entries. */
export function attention(s: Snapshot | undefined): number {
  return problemCount(s) + (s?.unread ?? 0);
}

/** A history row's mark: a problem opening or coming back, its end, or a plain message. */
export function kindTone(e: Entry): Tone {
  if (e.kind === "resolved") return "ok";
  if (e.kind === "sent") return e.severity === "alert" ? "warn" : "idle";
  return e.kind === "reminded" ? "warn" : "bad";
}

/** Whether any delivery attempt failed for reasons other than a channel filter. */
export function deliveryHasError(e: Entry): boolean {
  return e.deliveries.some((d) => !d.ok && !d.filtered);
}

/** What happened delivering an entry externally, or "" when every enabled channel took it. */
export function delivery(e: Entry): string {
  if (e.deliveries.length === 0) return "not sent";
  if (e.deliveries.every((d) => d.filtered)) return "not sent: filtered";
  return e.deliveries
    .flatMap((d) => {
      if (d.filtered) return [`${d.channel} filtered`];
      if (!d.ok) return [`${d.channel} failed`];
      return [];
    })
    .join(", ");
}

/** The body's first line, without a title it repeats. */
export function detail(e: Entry): string {
  const first = e.body.split("\n", 1)[0]?.trim() ?? "";
  if (first === e.title) return "";
  const prefix = `${e.title} · `;
  return first.startsWith(prefix) ? first.slice(prefix.length) : first;
}

const AUDIT_HREF: Record<string, string> = {
  disk: "/storage",
  smart: "/storage",
  daemon: "/settings/logs?unit=homelab-daemon",
  caddy: "/settings/logs?unit=caddy",
  "systemd-units": "/settings/logs",
  "cloudflare-tunnel": "/settings/tunnel",
  "db-snapshot": "/backups",
};

/** Where a notification row points in the dashboard, or null when no page fits. */
export function notificationHref(key: string | null | undefined, title = ""): string | null {
  const k = key?.trim() ?? "";
  if (k) {
    if (k.startsWith("stack:")) return `/stacks/${encodeURIComponent(k.slice("stack:".length))}`;
    if (k.startsWith("stack-install:")) return `/stacks/${encodeURIComponent(k.slice("stack-install:".length))}`;
    if (k.startsWith("stack-update:")) return `/stacks/${encodeURIComponent(k.slice("stack-update:".length))}`;
    if (k.startsWith("stack-rollback:")) return `/stacks/${encodeURIComponent(k.slice("stack-rollback:".length))}`;
    if (k.startsWith("backup:") || k.startsWith("backup-stopped:")) return `/backups/${encodeURIComponent(k.slice(k.indexOf(":") + 1))}`;
    if (k.startsWith("backup-stale:")) return `/backups/${encodeURIComponent(k.slice("backup-stale:".length))}`;
    if (k.startsWith("backup-coverage:")) return "/backups";
    if (k.startsWith("disk:") || k.startsWith("disk-remove:")) return "/storage";
    if (k.startsWith("image-release:") || k.startsWith("image-boot:") || k.startsWith("core-deploy:") || k.startsWith("host-rebuild:")) return "/settings/updates";
    if (k === "host:power") return "/settings/system";
    if (k.startsWith("churn:") || k.startsWith("unit:")) {
      const unit = k.slice(k.indexOf(":") + 1).replace(/\.service$/, "");
      return unit ? `/settings/logs?unit=${encodeURIComponent(unit)}` : "/settings/logs";
    }
    if (k === "daemon-crash") return "/settings/logs?unit=homelab-daemon";
    if (k.startsWith("audit:")) return AUDIT_HREF[k.slice("audit:".length)] ?? null;
    if (k.startsWith("security:")) return "/settings/security";
  }
  if (/^Security audit\b/.test(title)) return "/settings/security";
  if (/^Updated to\b/.test(title)) return "/settings/updates";
  if (/^Not backed up:/.test(title)) return "/backups";
  if (/^Daemon recovered from crash\b/.test(title)) return "/settings/logs?unit=homelab-daemon";
  const m = /^Stack ([a-z0-9-]+)(?:\b|:)/.exec(title);
  if (m) return `/stacks/${encodeURIComponent(m[1]!)}`;
  return null;
}

/** Where a log entry points in the dashboard, preferring its stored path when specific. */
export function entryHref(e: Pick<Entry, "path" | "key" | "title">): string | null {
  const p = e.path?.trim();
  if (p && p.startsWith("/") && p !== "/") return p;
  return notificationHref(e.key, e.title);
}
