/**
 * Server-Sent Events read from a stream by hand: EventSource only does GET,
 * and the assistant's question is a POST with the files in its body.
 */
export interface SSEvent {
  event: string;
  data: string;
}

/** A reader fed chunks of text; each push returns the events it completed. */
export function sseReader() {
  let buf = "";
  return {
    push(chunk: string): SSEvent[] {
      buf += chunk.replace(/\r\n?/g, "\n");
      const out: SSEvent[] = [];
      let end: number;
      while ((end = buf.indexOf("\n\n")) >= 0) {
        const block = buf.slice(0, end);
        buf = buf.slice(end + 2);
        let event = "message";
        const data: string[] = [];
        for (const line of block.split("\n")) {
          if (line.startsWith(":")) continue;
          const colon = line.indexOf(":");
          const field = colon < 0 ? line : line.slice(0, colon);
          let value = colon < 0 ? "" : line.slice(colon + 1);
          if (value.startsWith(" ")) value = value.slice(1);
          if (field === "event") event = value;
          else if (field === "data") data.push(value);
        }
        if (data.length) out.push({ event, data: data.join("\n") });
      }
      return out;
    },
  };
}

/** Reads a fetched event stream to its end, handing each event to onEvent. */
export async function readEvents(stream: unknown, onEvent: (event: string, data: string) => void) {
  if (!stream) throw new Error("The server sent no answer.");
  const reader = (stream as ReadableStream<Uint8Array>).getReader();
  const decoder = new TextDecoder();
  const sse = sseReader();
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    for (const ev of sse.push(decoder.decode(value, { stream: true }))) onEvent(ev.event, ev.data);
  }
}
