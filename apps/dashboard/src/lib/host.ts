/** Host settings (#96): the pure logic behind Settings › Host. */
import { api, call, type Schema } from "./api/client.ts";

export type HostSettings = Schema<"HostSettings">;
export type Opening = Schema<"Opening">;
export type Peer = Schema<"Peer">;
export type Route = Schema<"Route">;
export type DNSProvider = Schema<"Domain">["dns_provider"];

export const hostKeys = { settings: ["daemon", "host-settings"] as const };

export const fetchHostSettings = () => call<HostSettings>(api.GET("/host-settings"));

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
