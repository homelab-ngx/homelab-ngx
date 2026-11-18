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

export class LiveMetrics {
  frames = $state.raw<Frames>(emptyFrames());
  info = $state.raw<HostInfo | undefined>();
  connected = $state(false);
  #es: EventSource | undefined;

  open() {
    if (this.#es) return;
    const es = new EventSource("/api/metrics/stream");
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
    this.#es?.close();
    this.#es = undefined;
    this.connected = false;
  }
}
