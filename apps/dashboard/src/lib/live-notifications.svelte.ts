/**
 * Live notifications stream (GET /api/notifications/stream, Server-Sent Events).
 * Keeps the header bell snapshot and open tray history updated in real time and
 * falls back to 30s polling only when disconnected.
 */
import type { InfiniteData, QueryClient } from "@tanstack/svelte-query";
import { notificationKeys, type Entry, type Snapshot } from "./notifications.ts";

/** The part of an EventSource this reads; the demo has its own (src/demo). */
export interface NotificationStreamSource {
  addEventListener(type: string, fn: (e: Event) => void): void;
  close(): void;
  onerror: ((e: Event) => void) | null;
}

/** Inserts or updates entry in an infinite log query, keeping newest-first order. */
export function prependEntry(old: InfiniteData<Entry[], number | undefined> | undefined, entry: Entry): InfiniteData<Entry[], number | undefined> | undefined {
  if (!old || old.pages.length === 0) return old;
  let replaced = false;
  const pages = old.pages.map((page) =>
    page.map((existing) => {
      if (existing.id === entry.id) {
        replaced = true;
        return entry;
      }
      return existing;
    }),
  );
  if (!replaced) {
    pages[0] = [entry, ...pages[0]!];
  }
  return { ...old, pages };
}

export class LiveNotifications {
  connected = $state(false);
  #es: NotificationStreamSource | undefined;
  #wanted = false;
  #client: QueryClient | undefined;

  open(client: QueryClient, sourceFactory?: () => NotificationStreamSource) {
    this.#client = client;
    this.#wanted = true;
    if (this.#es) return;
    if (sourceFactory) {
      this.#listen(sourceFactory());
      return;
    }
    if (import.meta.env.MODE === "demo") {
      void import("../demo/index.ts").then((m) => {
        if (this.#wanted && !this.#es) this.#listen(m.demoNotifications());
      });
      return;
    }
    this.#listen(new EventSource("/api/notifications/stream"));
  }

  #listen(es: NotificationStreamSource) {
    es.addEventListener("snapshot", (e) => {
      const s = JSON.parse((e as MessageEvent<string>).data) as Snapshot;
      this.#client?.setQueryData(notificationKeys.snapshot, s);
      this.connected = true;
    });
    es.addEventListener("entry", (e) => {
      const entry = JSON.parse((e as MessageEvent<string>).data) as Entry;
      this.#client?.setQueryData<InfiniteData<Entry[], number | undefined>>(notificationKeys.log, (old) => prependEntry(old, entry));
      this.connected = true;
    });
    es.addEventListener("ping", () => {
      this.connected = true;
    });
    es.onerror = () => {
      this.connected = false;
    };
    this.#es = es;
  }

  close() {
    this.#wanted = false;
    this.#es?.close();
    this.#es = undefined;
    this.#client = undefined;
    this.connected = false;
  }
}

export const liveNotifications = new LiveNotifications();
