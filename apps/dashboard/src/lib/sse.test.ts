import { describe, expect, it } from "vitest";
import { sseReader } from "./sse.ts";

describe("sseReader", () => {
  it("joins chunks into events, across splits", () => {
    const r = sseReader();
    expect(r.push('event: text\ndata: {"text":"He')).toEqual([]);
    expect(r.push('l"}\n\nevent: text\r\ndata: {"text":"lo"}\r\n\r\n: keepalive\n\n')).toEqual([
      { event: "text", data: '{"text":"Hel"}' },
      { event: "text", data: '{"text":"lo"}' },
    ]);
    expect(r.push("data: a\ndata: b\n\n")).toEqual([{ event: "message", data: "a\nb" }]);
  });
});
