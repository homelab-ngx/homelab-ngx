/** System Settings' index: each tile's status line, from what the pages behind it read. */
import type { Schema, SecurityAuditItem } from "./api/client.ts";
import type { Tone } from "./components/Pill.svelte";
import { tailscaleState, type HostSettings, type TailscaleStatus } from "./host.ts";
import { failingOf, type Channel, type Snapshot } from "./notifications.ts";
import { POSTURE, RESULT } from "./security.ts";

/** A tile's status: a mark and a word, or plain muted text when tone is absent. */
export interface TileStatus {
  tone?: Tone;
  text: string;
}

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

/** Host: its name and time zone. */
export const hostLine = (s: HostSettings): TileStatus => ({ text: `${s.host.hostname || "homelab"} · ${s.host.timezone || "UTC"}` });

/** Clock: whether the host's time is set from a server. */
export const clockStatus = (s: HostSettings): TileStatus =>
  s.time_sync.synced ? { tone: "ok", text: "synced" } : { tone: "warn", text: "not synced" };

/** Console: the keyboard layout's name; none set is the image's US layout. */
export function consoleLine(s: HostSettings, layouts: Schema<"KeyboardLayout">[] | undefined): TileStatus {
  const k = s.console.keyboard;
  if (!k || k === "us") return { text: "English (US)" };
  return { text: layouts?.find((l) => l.name === k)?.description ?? k };
}

/** The Network band's state: whether the stored settings are in effect. */
export function appliedStatus(s: HostSettings): TileStatus {
  if (s.managed === "nixos") return { tone: "info", text: "nixos" };
  if (s.applied.errors.length > 0) return { tone: "bad", text: "not applied" };
  return { tone: "ok", text: "applied" };
}

/** Domain: the name the dashboard is served under. */
export const domainLine = (s: HostSettings): TileStatus => (s.domain.domain ? { text: s.domain.domain } : { tone: "idle", text: "not set" });

/** LAN: the subnets taken as the home network. */
export const lanLine = (s: HostSettings): TileStatus =>
  s.network.lan.length > 0 ? { text: s.network.lan.join(", ") } : { tone: "idle", text: "not set" };

/** Tailscale: off unless turned on, then the daemon's state of it. */
export function tailscaleStatus(s: HostSettings, st: TailscaleStatus | undefined): TileStatus {
  if (!s.tailscale.enabled) return { tone: "idle", text: "off" };
  const m = tailscaleState(st);
  return { tone: m.tone, text: m.word };
}

/** WireGuard: on and how many devices, or off. */
export const vpnStatus = (s: HostSettings): TileStatus =>
  s.wireguard.enabled ? { tone: "ok", text: `on · ${plural(s.wireguard.peers.length, "device")}` } : { tone: "idle", text: "off" };

/** Cloudflare Tunnel: on once it has its credentials. */
export const tunnelStatus = (s: HostSettings): TileStatus =>
  s.tunnel.enabled && s.tunnel.credentials_set ? { tone: "ok", text: `on · ${plural(s.tunnel.routes.length, "route")}` } : { tone: "idle", text: "off" };

/** Firewall: the openings besides the built-in ones. */
export const firewallLine = (s: HostSettings): TileStatus => ({ text: s.firewall.openings.length > 0 ? plural(s.firewall.openings.length, "opening") : "no openings" });

/** SSH: the keys that may log in as root. */
export const sshLine = (s: HostSettings): TileStatus => {
  const n = s.host.ssh_keys?.length ?? 0;
  return n > 0 ? { text: plural(n, "key") } : { tone: "idle", text: "no keys" };
};

/** Notifications: failing channels first, else how many deliver. */
export function notificationsStatus(channels: Channel[] | undefined, snapshot: Snapshot | undefined): TileStatus | undefined {
  if (!channels) return undefined;
  const failing = failingOf(snapshot).length;
  if (failing > 0) return { tone: "bad", text: `${failing} failing` };
  const on = channels.filter((c) => c.enabled).length;
  return on > 0 ? { tone: "ok", text: plural(on, "channel") } : { tone: "idle", text: "no channels" };
}

/** Security audit: the last one's posture, else its result, else never run. */
export function auditStatus(audits: SecurityAuditItem[] | undefined): TileStatus | undefined {
  if (!audits) return undefined;
  const last = audits[0];
  if (!last) return { tone: "idle", text: "never" };
  if (last.posture) return { tone: POSTURE[last.posture], text: last.posture };
  return { tone: RESULT[last.result].tone, text: RESULT[last.result].label };
}
