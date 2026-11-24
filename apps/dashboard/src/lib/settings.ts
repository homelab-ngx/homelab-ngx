/**
 * System Settings' index: its tiles, each a page of its own, and their
 * badges, which the daemon reports in one answer (GET /settings/status).
 */
import { Bell, BrickWall, CircleUser, Clock, Cloud, Gauge, Globe, House, Keyboard, KeyRound, Network, Power, RefreshCw, ScrollText, ShieldCheck, ShieldHalf, Sparkles, Waypoints } from "@lucide/svelte";
import { api, call, type Schema } from "./api/client.ts";

/** A tile's badge: a tone, a count, both, or neither. */
export type TileStatus = Schema<"TileStatus">;
export type TileState = NonNullable<TileStatus["state"]>;

export const settingsStatusKey = ["daemon", "settings", "status"] as const;
export const fetchSettingsStatus = () => call<Schema<"SettingsStatus">>(api.GET("/settings/status"));

type Icon = typeof House;

export interface Tile {
  /** The daemon's name for the tile in GET /settings/status. */
  key: string;
  name: string;
  href: string;
  icon: Icon;
  /** The core host's own software, in core violet. */
  core?: boolean;
  /** The state in words, for the link's spoken name, where the default word does not fit. */
  words?: Partial<Record<TileState, string>>;
  /** What the count counts: one, and more than one. */
  noun?: [string, string];
}

export interface Section {
  id: string;
  title: string;
  tiles: Tile[];
}

const notApplied = { bad: "not applied" };
const tile = (key: string, name: string, icon: Icon, more: Partial<Tile> = {}): Tile => ({ key, name, icon, href: `/settings/${key}`, ...more });

export const SECTIONS: Section[] = [
  {
    id: "host",
    title: "This host",
    tiles: [
      tile("host", "Host", House, { words: notApplied }),
      tile("clock", "Clock", Clock, { words: { warn: "not synced" } }),
      tile("console", "Console", Keyboard),
      tile("power-profile", "Power profile", Gauge),
      tile("updates", "Updates", RefreshCw, { core: true, words: { info: "update waiting", bad: "fell back" } }),
      tile("notifications", "Notifications", Bell, { words: { ok: "on", bad: "failing", off: "no channels" }, noun: ["channel", "channels"] }),
      tile("logs", "Logs", ScrollText),
      tile("power", "Power", Power, { words: { info: "scheduled" } }),
    ],
  },
  {
    id: "net",
    title: "Network",
    tiles: [
      tile("domain", "Domain", Globe, { words: { off: "not set", ...notApplied } }),
      tile("lan", "LAN", Network, { words: { off: "not set" } }),
      tile("tailscale", "Tailscale", Waypoints, { words: { ok: "connected", warn: "needs login", info: "starting", ...notApplied } }),
      tile("wireguard", "WireGuard VPN", ShieldHalf, { words: { ok: "on" }, noun: ["device", "devices"] }),
      tile("tunnel", "Cloudflare Tunnel", Cloud, { words: { ok: "on", ...notApplied }, noun: ["route", "routes"] }),
      tile("firewall", "Firewall", BrickWall, { words: notApplied, noun: ["opening", "openings"] }),
      tile("ssh", "SSH", KeyRound, { words: { off: "no keys", ...notApplied }, noun: ["key", "keys"] }),
    ],
  },
  {
    id: "ai",
    title: "Assistant",
    tiles: [
      tile("assistant", "AI assistant", Sparkles, { words: { ok: "on" } }),
      tile("security", "Security audit", ShieldCheck, { words: { ok: "good", info: "fair", warn: "weak", bad: "poor", off: "never run" } }),
    ],
  },
  { id: "browser", title: "This browser", tiles: [tile("account", "Account", CircleUser)] },
];

const WORDS: Record<TileState, string> = { ok: "ok", warn: "warning", bad: "failed", info: "pending", off: "off" };

/** The tile link's spoken name: its name, then the badge in words ("WireGuard VPN, on, 2 devices"). */
export function tileLabel(t: Tile, st: TileStatus | undefined): string {
  const parts = [t.name];
  if (st?.state) parts.push(t.words?.[st.state] ?? WORDS[st.state]);
  if (st?.count !== undefined) {
    const [one, many] = t.noun ?? ["item", "items"];
    parts.push(`${st.count} ${st.count === 1 ? one : many}`);
  }
  return parts.join(", ");
}

/** The tiles that were cards on the General and Network pages, by their old anchor. */
const MOVED = new Set(["host", "clock", "console", "domain", "lan", "tailscale", "wireguard", "tunnel", "firewall", "ssh"]);

/** Where an old /settings/general or /settings/network link goes: its anchor's own page, else the index. */
export function movedTile(hash: string): string {
  const key = hash.replace(/^#/, "");
  return MOVED.has(key) ? `/settings/${key}` : "/settings";
}
