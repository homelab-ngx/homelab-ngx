/** Host settings (#96): the pure logic behind System Settings › General and › Network. */
import { api, call, type Schema, type Stack } from "./api/client.ts";
import type { Tone } from "./components/Pill.svelte";

export type HostSettings = Schema<"HostSettings">;
export type Opening = Schema<"Opening">;
export type Peer = Schema<"Peer">;
export type Route = Schema<"Route">;
export type DNSProvider = Schema<"Domain">["dns_provider"];

export type TailscaleStatus = Schema<"TailscaleStatus">;
export type TailscalePeer = Schema<"TailscalePeer">;
export type TailscalePeers = Schema<"TailscalePeers">;
export type TailscaleLoginInput = Schema<"TailscaleLoginInput">;
export type TunnelStatus = Schema<"TunnelStatus">;
export type RouteStatus = Schema<"RouteStatus">;
export type Login = Schema<"Login">;
export type AddedDevice = Schema<"AddedDevice">;

export const hostKeys = {
  settings: ["daemon", "host-settings"] as const,
  tailscale: ["daemon", "host-settings", "tailscale"] as const,
  tailscalePeers: ["daemon", "host-settings", "tailscale", "peers"] as const,
  tunnelLogin: ["daemon", "host-settings", "tunnel-login"] as const,
  tunnelStatus: ["daemon", "host-settings", "tunnel-status"] as const,
  keyboardLayouts: ["daemon", "host-settings", "keyboard-layouts"] as const,
};

export const fetchHostSettings = () => call<HostSettings>(api.GET("/host-settings"));
export const fetchTailscale = () => call<TailscaleStatus>(api.GET("/host-settings/tailscale/status"));
export const fetchTailscalePeers = () => call<TailscalePeers>(api.GET("/host-settings/tailscale/peers"));
export const fetchTunnelLogin = () => call<Login>(api.GET("/host-settings/tunnel/login"));
export const fetchTunnelStatus = () => call<TunnelStatus>(api.GET("/host-settings/tunnel/status"));

const TAILSCALE_EXPIRY_WARN_DAYS = 14;

/** Whole days remaining until the node key expires (negative if elapsed), or null when expiry is off. */
export function tailscaleExpiryDays(st: TailscaleStatus | undefined, now = Date.now()): number | null {
  if (!st?.key_expiry) return null;
  const ms = Date.parse(st.key_expiry);
  if (Number.isNaN(ms)) return null;
  return Math.ceil((ms - now) / 86_400_000);
}

/** Pending admin-console approvals reported for the host. */
export function tailscalePending(st: TailscaleStatus | undefined): { routes: number; exitNode: boolean; device: boolean; any: boolean } {
  const device = st?.state === "needs-approval";
  const routes = st?.state === "running" ? (st.routes ?? []).filter((r) => !r.approved).length : 0;
  const exitNode = (st?.state === "running" || st?.state === "needs-approval") && st.exit_node === "pending";
  return { routes, exitNode, device, any: device || routes > 0 || exitNode };
}

/** Short summary of pending admin-console approvals, or "". */
export function tailscalePendingMessage(st: TailscaleStatus | undefined): string {
  const p = tailscalePending(st);
  if (!p.any) return "";
  if (p.device) return "Device waits for approval in the admin console.";
  const parts: string[] = [];
  if (p.routes > 0) parts.push(`${p.routes} ${p.routes === 1 ? "subnet" : "subnets"}`);
  if (p.exitNode) parts.push("the exit node");
  const verb = p.routes === 1 && !p.exitNode ? "waits" : "wait";
  return `${parts.join(" and ")} ${verb} for approval in the admin console.`;
}

/** Polling cadence in milliseconds while waiting on login, approvals, or warnings. */
export function tailscaleNeedsPoll(st: TailscaleStatus | undefined, now = Date.now()): 2000 | 30000 | false {
  if (!st) return false;
  if (st.state === "starting" || !!st.auth_url) return 2000;
  if (tailscalePending(st).any) return 30000;
  if (st.state === "running") {
    const days = tailscaleExpiryDays(st, now);
    if ((days !== null && days <= TAILSCALE_EXPIRY_WARN_DAYS) || (st.health?.length ?? 0) > 0) return 30000;
  }
  return false;
}

/** Tailscale's state as a status mark and word. */
export function tailscaleState(
  st: TailscaleStatus | undefined,
  now = Date.now(),
): { tone: "ok" | "warn" | "bad" | "info" | "idle"; word: string } {
  switch (st?.state) {
    case "running": {
      if (st.auth_url) return { tone: "info", word: "waiting for login" };
      const days = tailscaleExpiryDays(st, now);
      if (days !== null && days <= 0) return { tone: "bad", word: "key expired" };
      if (tailscalePending(st).any) return { tone: "warn", word: "awaiting approval" };
      if (days !== null && days <= TAILSCALE_EXPIRY_WARN_DAYS) return { tone: "warn", word: "key expiring" };
      if ((st.health?.length ?? 0) > 0) return { tone: "warn", word: "warning" };
      return { tone: "ok", word: "connected" };
    }
    case "expired":
      return { tone: "bad", word: "key expired" };
    case "stopped":
      return { tone: "warn", word: "stopped" };
    case "needs-login":
      return st.auth_url ? { tone: "info", word: "waiting for login" } : { tone: "warn", word: "logged out" };
    case "needs-approval":
      return { tone: "warn", word: "awaiting approval" };
    case "starting":
      return { tone: "info", word: "starting" };
  }
  return { tone: "idle", word: "off" };
}

/** Non-added IPv4 subnets from LAN and wired interfaces, as quick-pick buttons. */
export function subnetChoices(
  s: Pick<HostSettings, "network" | "wired">,
  current: readonly string[],
): { cidr: string; label: string }[] {
  const existing = new Set(current.map((c) => c.trim()).filter(Boolean));
  const seen = new Set<string>();
  const out: { cidr: string; label: string }[] = [];
  const add = (cidr: string, label: string) => {
    const c = cidr.trim();
    if (!c || existing.has(c) || seen.has(c)) return;
    seen.add(c);
    out.push({ cidr: c, label });
  };
  for (const c of s.network.lan ?? []) add(c, "LAN");
  for (const w of s.wired ?? []) for (const c of w.subnets ?? []) add(c, w.name);
  return out;
}

/** Whether s is a valid IPv4 CIDR prefix such as 192.168.1.0/24. */
export function isIPv4CIDR(s: string): boolean {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(s.trim());
  if (!m) return false;
  const prefix = Number(m[5]);
  if (prefix < 1 || prefix > 32) return false;
  return [m[1], m[2], m[3], m[4]].every((o) => Number(o) <= 255 && String(Number(o)) === o);
}

/** Validation message for offered subnet rows, or "". */
export function subnetProblem(routes: readonly string[]): string {
  const seen = new Set<string>();
  for (const raw of routes) {
    const c = raw.trim();
    if (!c) continue;
    if (!isIPv4CIDR(c)) return "Enter an IPv4 subnet such as 192.168.1.0/24.";
    if (seen.has(c)) return "Remove duplicate subnets.";
    seen.add(c);
  }
  return "";
}

/** Validation message for an optional custom control server URL, or "". */
export function loginServerProblem(raw: string): string {
  const s = raw.trim();
  if (!s) return "";
  if (/[\s\u0000-\u001f\u007f]/.test(s)) return "Remove spaces from the server URL.";
  try {
    const u = new URL(s);
    if (u.protocol !== "https:" || !u.hostname || u.username || u.password || u.search || u.hash) {
      return "Use an https:// server URL.";
    }
    return "";
  } catch {
    return "Use an https:// server URL.";
  }
}

/** The VPN's UDP port. */
export const vpnPort = (w: { listen_port?: number }) => w.listen_port || 51820;

/** The host's address on the LAN: the static one, else a wired interface's. */
export function lanAddress(s: Pick<HostSettings, "network" | "wired">): string {
  if (s.network.ipv4?.mode === "static" && s.network.ipv4.address) return s.network.ipv4.address.replace(/\/\d+$/, "");
  return s.wired.flatMap((w) => w.addresses ?? [])[0] ?? "";
}

/** The name dynamic DNS keeps, as the daemon writes it, or "" without a domain. */
export function ddnsName(s: Pick<HostSettings, "domain">, label: string): string {
  const d = s.domain.domain;
  if (!d) return "";
  if (s.domain.dns_provider === "duckdns") return d;
  return `${label || "vpn"}.${d}`;
}

/** The credential fields each DNS provider takes. */
export const DNS_FIELDS: Record<string, { key: string; label: string }[]> = {
  cloudflare: [{ key: "api_token", label: "API token" }],
  porkbun: [
    { key: "api_key", label: "API key" },
    { key: "api_secret_key", label: "Secret API key" },
  ],
  duckdns: [{ key: "api_token", label: "Token" }],
  route53: [
    { key: "access_key_id", label: "Access key id" },
    { key: "secret_access_key", label: "Secret access key" },
  ],
};

export const DNS_LABEL: Record<string, string> = {
  "": "None: local certificate",
  cloudflare: "Cloudflare",
  porkbun: "Porkbun",
  duckdns: "DuckDNS",
  route53: "Route 53",
};

/** A list typed into one field: separated by commas, spaces or new lines. */
export function splitList(s: string): string[] {
  return s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);
}

export const joinList = (a: readonly string[] | null | undefined) => (a ?? []).join(", ");

/** The dashboard's address under the domain, or "" without one. */
export function dashboardHost(d: { domain: string; dashboard_label: string }): string {
  return d.domain ? `${d.dashboard_label || "homelab"}.${d.domain}` : "";
}

/** The WireGuard client config for a peer, without its private key. */
export function peerConfig(wg: { public_key?: string; endpoint?: string; address?: string }, peer: Peer): string {
  const net = (wg.address ?? "").replace(/\.\d+\/(\d+)$/, ".0/$1");
  return [
    "[Interface]",
    "PrivateKey = <the device's own key>",
    `Address = ${peer.address}`,
    "",
    "[Peer]",
    `PublicKey = ${wg.public_key ?? ""}`,
    `Endpoint = ${wg.endpoint || "<host>:51820"}`,
    `AllowedIPs = ${net}`,
    "PersistentKeepalive = 25",
  ].join("\n");
}

/** Whether two values differ, for an Apply that stays off until something changed. */
export const changed = (a: unknown, b: unknown) => JSON.stringify(a) !== JSON.stringify(b);

/** The tunnel's live state as a status mark and word. */
export function tunnelPill(
  st: TunnelStatus | undefined,
  login: Login | undefined,
  enabled: boolean,
  creds: boolean,
  managed: HostSettings["managed"] = "image",
): { tone: Tone; word: string } {
  if (login?.state === "waiting") return { tone: "info", word: "connecting" };
  if (!enabled || (managed === "image" && !creds)) return { tone: "idle", word: "off" };
  switch (st?.state) {
    case "connected":
      return { tone: "ok", word: "connected" };
    case "degraded":
      return { tone: "warn", word: "degraded" };
    case "down":
      return { tone: "bad", word: "down" };
    case "starting":
      return { tone: "info", word: "starting" };
  }
  return { tone: "info", word: "starting" };
}

/** One tunnel route's state pill, prioritizing DNS errors and unintended exposure. */
export function routePill(rs: RouteStatus | undefined, tunnelOn: boolean): { tone: Tone; word: string } {
  if (rs?.dns === "failed") return { tone: "bad", word: "dns failed" };
  if (!tunnelOn) return { tone: "idle", word: "off" };
  if (!rs) return { tone: "info", word: "checking" };
  if (rs.protection === "open") return { tone: "warn", word: "open" };
  if (rs.no_target) return { tone: "warn", word: "no target" };
  if (rs.protection === "unreachable") return { tone: "bad", word: "unreachable" };
  if (rs.dns === "pending" || rs.protection === "checking") return { tone: "info", word: "checking" };
  if (rs.protection === "protected") return { tone: "ok", word: "protected" };
  if (rs.protection === "public") return { tone: "ok", word: "public" };
  if (rs.protection === "not-served") return { tone: "idle", word: "not served" };
  return { tone: "idle", word: "off" };
}

/** Shortens a tunnel UUID for display: 6f1c…e2a0. */
export function shortTunnelId(id: string | undefined): string {
  if (!id) return "";
  return id.length > 12 ? `${id.slice(0, 4)}…${id.slice(-4)}` : id;
}

/** Minutes remaining before a waiting login link expires. */
export function loginMinutesLeft(expiresAt: string | undefined, now = Date.now()): number {
  if (!expiresAt) return 0;
  const ms = Date.parse(expiresAt) - now;
  return Number.isFinite(ms) && ms > 0 ? Math.max(1, Math.ceil(ms / 60_000)) : 0;
}

/** Whether hostname is inside zone (exact match or subdomain). */
export function inZone(hostname: string, zone: string): boolean {
  const h = hostname.trim().toLowerCase().replace(/\.$/, "");
  const z = zone.trim().toLowerCase().replace(/\.$/, "");
  return Boolean(z) && (h === z || h.endsWith(`.${z}`));
}

/** Whether a tunnel target names the dashboard itself. */
export function isDashboardTarget(target: string, d: { domain: string; dashboard_label: string }): boolean {
  const t = target.trim().toLowerCase().replace(/\.$/, "");
  if (!t) return false;
  const label = (d.dashboard_label || "homelab").trim().toLowerCase();
  if (!t.includes(".")) return t === label;
  const host = dashboardHost(d).toLowerCase();
  return Boolean(host) && t === host;
}

export interface StackRouteOption {
  stack: string;
  stackTitle: string;
  host: string;
  target: string;
}

/** Approved stack routes offered in the Tunnel route editor's Serves picker. */
export function approvedStackTargets(stacks: Stack[] | undefined, domain: { domain: string; dashboard_label: string }): StackRouteOption[] {
  const suffix = domain.domain ? `.${domain.domain.toLowerCase()}` : "";
  const out: StackRouteOption[] = [];
  const seen = new Set<string>();
  for (const s of stacks ?? []) {
    const title = s.label || s.name;
    for (const r of s.approved?.manifest?.routes ?? []) {
      const host = r.host.toLowerCase();
      if (!host || seen.has(host) || isDashboardTarget(host, domain)) continue;
      seen.add(host);
      const target = suffix && host.endsWith(suffix) ? host.slice(0, -suffix.length) : host;
      out.push({ stack: s.name, stackTitle: title, host, target });
    }
  }
  return out.sort((a, b) => a.stackTitle.localeCompare(b.stackTitle) || a.host.localeCompare(b.host));
}
