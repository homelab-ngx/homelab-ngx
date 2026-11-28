import { QueryClient, type InfiniteData } from "@tanstack/svelte-query";
import { describe, expect, it } from "vitest";
import { LiveNotifications, prependEntry, type NotificationStreamSource } from "./live-notifications.svelte.ts";
import { notificationKeys, type Entry, type Snapshot } from "./notifications.ts";

class FakeSource implements NotificationStreamSource {
  listeners = new Map<string, (e: Event) => void>();
  onerror: ((e: Event) => void) | null = null;
  closed = false;

  addEventListener(type: string, fn: (e: Event) => void) {
    this.listeners.set(type, fn);
  }
  close() {
    this.closed = true;
  }
  emit(type: string, payload: unknown) {
    this.listeners.get(type)?.(new MessageEvent(type, { data: JSON.stringify(payload) }));
  }
  fail() {
    this.onerror?.(new Event("error"));
  }
}

const makeEntry = (id: number, title = `Entry ${id}`): Entry => ({
  id,
  at: "2026-11-28T10:00:00Z",
  key: null,
  kind: "sent",
  severity: "info",
  title,
  body: "",
  deliveries: [],
});

describe("prependEntry", () => {
  it("keeps uninitialized log cache undefined and avoids duplicate ids", () => {
    expect(prependEntry(undefined, makeEntry(1))).toBeUndefined();

    const initial: InfiniteData<Entry[], number | undefined> = {
      pages: [[makeEntry(2), makeEntry(1)]],
      pageParams: [undefined],
    };
    const next = prependEntry(initial, makeEntry(3));
    expect(next?.pages[0]?.map((e) => e.id)).toEqual([3, 2, 1]);

    const updated = prependEntry(next, makeEntry(2, "Updated 2"));
    expect(updated?.pages[0]?.map((e) => e.id)).toEqual([3, 2, 1]);
    expect(updated?.pages[0]?.find((e) => e.id === 2)?.title).toBe("Updated 2");
  });
});

describe("LiveNotifications", () => {
  it("updates snapshot and prepends live log entries over SSE", () => {
    const client = new QueryClient();
    const fake = new FakeSource();
    const live = new LiveNotifications();

    const seeded: InfiniteData<Entry[], number | undefined> = {
      pages: [[makeEntry(4)]],
      pageParams: [undefined],
    };
    client.setQueryData(notificationKeys.log, seeded);

    live.open(client, () => fake);
    expect(live.connected).toBe(false);

    const snap: Snapshot = {
      open: {},
      failing_channels: {},
      history: [],
      read_up_to: 4,
      latest_id: 4,
      unread: 0,
    };
    fake.emit("snapshot", snap);
    expect(live.connected).toBe(true);
    expect(client.getQueryData(notificationKeys.snapshot)).toEqual(snap);

    fake.emit("entry", makeEntry(5, "Built image 0.9.0"));
    fake.emit("snapshot", { ...snap, latest_id: 5, unread: 1 });
    expect(client.getQueryData<InfiniteData<Entry[], number | undefined>>(notificationKeys.log)?.pages[0]?.map((e) => e.id)).toEqual([5, 4]);
    expect(client.getQueryData<Snapshot>(notificationKeys.snapshot)?.unread).toBe(1);

    fake.fail();
    expect(live.connected).toBe(false);
    fake.emit("ping", { time: "2026-11-28T10:01:00Z" });
    expect(live.connected).toBe(true);

    live.close();
    expect(fake.closed).toBe(true);
    expect(live.connected).toBe(false);
  });
});
