import { describe, expect, it } from "vitest";
import { channelDraft, channelInput, channelProblem, channelState, channelWhere, type Channel } from "./notifications.ts";

const ntfy: Channel = { id: 1, name: "Phone", kind: "ntfy", enabled: true, server: "https://ntfy.sh", topic: "homelab", has_token: false, created_at: "" };
const hook: Channel = { id: 2, name: "Hub", kind: "webhook", enabled: false, url: "https://192.0.2.1:8788/homelab/notify", cert_sha256: "a".repeat(64), has_token: true, created_at: "" };

describe("channel form", () => {
  it("keeps the default server out of the field and the token empty", () => {
    expect(channelDraft(ntfy)).toMatchObject({ server: "", topic: "homelab", token: "" });
    expect(channelDraft(hook)).toMatchObject({ kind: "webhook", pin: "a".repeat(64) });
  });
  it("sends only the kind's fields and no empty token", () => {
    expect(channelInput({ ...channelDraft(null), name: " Phone ", topic: "t" })).toEqual({ name: "Phone", kind: "ntfy", topic: "t", token: undefined, clear_token: undefined, server: undefined });
    expect(channelInput(channelDraft(hook))).toMatchObject({ kind: "webhook", url: hook.url, cert_sha256: hook.cert_sha256, token: undefined });
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
  it("names where a channel delivers", () => {
    expect(channelWhere(ntfy)).toBe("homelab");
    expect(channelWhere({ ...ntfy, server: "https://ntfy.example.com" })).toBe("homelab on ntfy.example.com");
    expect(channelWhere(hook)).toBe(hook.url);
  });
  it("puts a test result before the stored state", () => {
    expect(channelState(ntfy, false, undefined)).toEqual({ tone: "ok", label: "on" });
    expect(channelState(ntfy, true, undefined).label).toBe("failing");
    expect(channelState(hook, false, undefined).label).toBe("off");
    expect(channelState(hook, false, { ok: false, error: "x" }).label).toBe("test failed");
  });
});
