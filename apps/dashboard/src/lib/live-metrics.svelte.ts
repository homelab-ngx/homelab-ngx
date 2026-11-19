/**
 * The live metrics stream (GET /api/metrics/stream, Server-Sent Events). The
 * first event is the last 15 minutes, every later one a new point. The page
 * opens it while visible and closes it while hidden; on a reconnect the
 * snapshot refills the window.
 */
import {
  appendFrames,
  emptyFrames,
  LIVE_SPAN,
  type Frames,
  type HostInfo,
  type MetricsSample,
  type Snapshot,
} from "./metrics.ts";

/** The part of an EventSource this reads; the demo has its own (src/demo). */
interface Source {
  addEventListener(type: string, fn: (e: Event) => void): void;
  close(): void;
  onerror: ((e: Event) => void) | null;
}

export class LiveMetrics {
  frames = $state.raw<Frames>(emptyFrames());
  info = $state.raw<HostInfo | undefined>();
  connected = $state(false);
  #es: Source | undefined;
  #wanted = false;

  open() {
    this.#wanted = true;
    if (this.#es) return;
    // The demo has no daemon to stream from (src/demo). The condition is
    // written out so that any other build drops the import.
    if (import.meta.env.MODE === "demo") {
      void import("../demo/index.ts").then((m) => {
        if (this.#wanted && !this.#es) this.#listen(m.demoMetrics());
      });
      return;
    }
    this.#listen(new EventSource("/api/metrics/stream"));
  }

  #listen(es: Source) {
    es.addEventListener("snapshot", (e) => {
      const s = JSON.parse((e as MessageEvent<string>).data) as Snapshot;
      this.info = s.info;
      this.frames = s.frames;
      this.connected = true;
    });
    es.addEventListener("sample", (e) => {
      const s = JSON.parse((e as MessageEvent<string>).data) as MetricsSample;
      this.frames = appendFrames(this.frames, s.frames, LIVE_SPAN);
    });
    // EventSource reconnects by itself; the next snapshot sets connected.
    es.onerror = () => (this.connected = false);
    this.#es = es;
  }

  close() {
    this.#wanted = false;
    this.#es?.close();
    this.#es = undefined;
    this.connected = false;
  }
}
