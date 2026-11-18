import { describe, expect, it } from "vitest";
import { segments } from "./ansi";

describe("segments", () => {
  it("keeps plain text as one segment", () => {
    expect(segments("hello")).toEqual([{ text: "hello", cls: "" }]);
  });

  it("colours text between SGR codes and resets", () => {
    expect(segments("\x1b[32m[Nest] 324  - \x1b[39mdate \x1b[33m[Router]\x1b[39m done")).toEqual([
      { text: "[Nest] 324  - ", cls: "text-ok" },
      { text: "date ", cls: "" },
      { text: "[Router]", cls: "text-warn" },
      { text: " done", cls: "" },
    ]);
  });

  it("handles bold, bright colours and a full reset", () => {
    expect(segments("\x1b[1;91mERR\x1b[0m ok")).toEqual([
      { text: "ERR", cls: "text-bad font-semibold" },
      { text: " ok", cls: "" },
    ]);
  });

  it("drops non-colour escapes and skips 256-colour arguments", () => {
    expect(segments("\x1b[2K\x1b[38;5;196mred?\x1b[m x")).toEqual([{ text: "red? x", cls: "" }]);
  });
});
