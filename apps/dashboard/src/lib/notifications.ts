/** Notification channels: the pure logic behind the Channels section and its form. */
import type { Schema } from "./api/client.ts";
import type { Tone } from "./components/Pill.svelte";

export type Channel = Schema<"Channel">;
export type ChannelInput = Schema<"ChannelInput">;
export type ChannelKind = Channel["kind"];

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
  };
}

/** The request body for d; enabled stays as it is. */
export function channelInput(d: ChannelDraft): ChannelInput {
  const base = { name: d.name.trim(), kind: d.kind, token: d.token.trim() || undefined, clear_token: d.clearToken || undefined };
  return d.kind === "ntfy"
    ? { ...base, server: d.server.trim() || undefined, topic: d.topic.trim() }
    : { ...base, url: d.url.trim(), cert_sha256: d.pin.trim() || undefined };
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
