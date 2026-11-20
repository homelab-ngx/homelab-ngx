/** Host settings (#96): the pure logic behind System Settings › Network. */
import { api, call, type Schema } from "./api/client.ts";

export type HostSettings = Schema<"HostSettings">;
export type Opening = Schema<"Opening">;
export type Peer = Schema<"Peer">;
export type Route = Schema<"Route">;
export type DNSProvider = Schema<"Domain">["dns_provider"];

export type TailscaleStatus = Schema<"TailscaleStatus">;
export type Login = Schema<"Login">;
export type AddedDevice = Schema<"AddedDevice">;

export const hostKeys = {
  settings: ["daemon", "host-settings"] as const,
  tailscale: ["daemon", "host-settings", "tailscale"] as const,
  tunnelLogin: ["daemon", "host-settings", "tunnel-login"] as const,
};

export const fetchHostSettings = () => call<HostSettings>(api.GET("/host-settings"));
export const fetchTailscale = () => call<TailscaleStatus>(api.GET("/host-settings/tailscale/status"));
export const fetchTunnelLogin = () => call<Login>(api.GET("/host-settings/tunnel/login"));

/** Tailscale's state as a status mark and word. */
export function tailscaleState(st: TailscaleStatus | undefined): { tone: "ok" | "warn" | "info" | "idle"; word: string } {
  switch (st?.state) {
    case "running":
      return { tone: "ok", word: "connected" };
    case "needs-login":
      return { tone: "warn", word: "logged out" };
    case "needs-approval":
      return { tone: "warn", word: "awaiting approval" };
    case "starting":
      return { tone: "info", word: "starting" };
  }
  return { tone: "idle", word: "off" };
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
