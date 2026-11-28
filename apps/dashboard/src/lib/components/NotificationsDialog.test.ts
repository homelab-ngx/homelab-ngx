import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client.ts";
import type { Entry, Snapshot } from "../notifications.ts";
import Harness from "./NotificationsDialogHarness.test.svelte";

afterEach(cleanup);

describe("NotificationsDialog", () => {
  it("links open problems and history entries and expands truncated text", async () => {
    const logEntries: Entry[] = [
      {
        id: 12,
        at: "2026-11-23T10:00:00Z",
        key: "stack:media",
        kind: "opened",
        severity: "alert",
        title: "Stack media failed with a very long headline that truncates",
        body: "Stack media failed\nDetailed container error line",
        deliveries: [{ channel: "ntfy", ok: false, error: "502 bad gateway", at: "2026-11-23T10:00:01Z" }],
      },
      {
        id: 11,
        at: "2026-11-22T09:00:00Z",
        key: null,
        kind: "sent",
        severity: "info",
        title: "Updated to 0.2.5",
        body: "Image updated cleanly.",
        deliveries: [],
      },
    ];
    vi.spyOn(api, "GET").mockImplementation(async (path: string) => {
      if (path === "/notifications/log") return { data: logEntries, response: new Response(null, { status: 200 }) } as never;
      return { data: { provider: "none" }, response: new Response(null, { status: 200 }) } as never;
    });

    const snapshot: Snapshot = {
      history: [],
      failing_channels: {},
      open: { "backup:4": { count: 1, opened_at: "2026-11-23T10:00:00Z", title: "Backup 4 failed" } },
      latest_id: 12,
      read_up_to: 12,
      unread: 0,
    };
    const onClose = vi.fn();

    render(Harness, { props: { client: new QueryClient(), snapshot, onClose } });

    const problemLink = await screen.findByRole("link", { name: "Backup 4 failed" });
    expect(problemLink.getAttribute("href")).toBe("/backups/4");

    await screen.findByText("Stack media failed with a very long headline that truncates");
    const openLinks = screen.getAllByRole("link", { name: "Open" });
    expect(openLinks.map((a) => a.getAttribute("href"))).toEqual(["/stacks/media", "/settings/updates"]);

    expect(screen.queryByText(/Detailed container error line/)).toBeNull();
    const expandBtn = screen.getByRole("button", { name: /Stack media failed/ });
    await fireEvent.click(expandBtn);
    expect(screen.getByText(/Detailed container error line/)).toBeTruthy();

    await fireEvent.click(problemLink);
    expect(onClose).toHaveBeenCalled();
    vi.restoreAllMocks();
  });

  it("marks entries read on open, shows unread dots, uses entry.path, and formats filtered delivery without error tone", async () => {
    const logEntries: Entry[] = [
      {
        id: 14,
        at: "2026-11-28T10:00:00Z",
        key: null,
        kind: "sent",
        event: "host.power",
        path: "/settings/system",
        severity: "info",
        title: "Host rebooting",
        body: "",
        deliveries: [{ channel: "Phone", ok: false, filtered: true, error: "", at: "2026-11-28T10:00:01Z" }],
      },
      {
        id: 10,
        at: "2026-11-27T10:00:00Z",
        key: "stack:media",
        kind: "sent",
        path: "/",
        severity: "info",
        title: "Stack media updated",
        body: "",
        deliveries: [{ channel: "Phone", ok: true, error: "", at: "2026-11-27T10:00:01Z" }],
      },
    ];
    vi.spyOn(api, "GET").mockImplementation(async (path: string) => {
      if (path === "/notifications/log") return { data: logEntries, response: new Response(null, { status: 200 }) } as never;
      return { data: { provider: "none" }, response: new Response(null, { status: 200 }) } as never;
    });
    const postSpy = vi.spyOn(api, "POST").mockResolvedValue({
      data: { read_up_to: 14, unread: 0 },
      response: new Response(null, { status: 200 }),
    } as never);

    const snapshot: Snapshot = {
      history: [],
      failing_channels: {},
      open: {},
      latest_id: 14,
      read_up_to: 12,
      unread: 1,
    };

    render(Harness, { props: { client: new QueryClient(), snapshot, onClose: vi.fn() } });

    await screen.findByText("Host rebooting");
    expect(postSpy).toHaveBeenCalledWith("/notifications/read", { body: { up_to: 14 } });

    expect(screen.getAllByLabelText("Unread")).toHaveLength(1);
    const filteredText = screen.getByText("not sent: filtered");
    expect(filteredText.className).not.toContain("text-bad");

    const openLinks = screen.getAllByRole("link", { name: "Open" });
    expect(openLinks.map((a) => a.getAttribute("href"))).toEqual(["/settings/system", "/stacks/media"]);
    vi.restoreAllMocks();
  });
});
