import { describe, expect, it } from "vitest";
import { formatLogLines, levelWord } from "./logs";

describe("formatLogLines", () => {
  it("strips ANSI escapes and formats timestamp, level, and message", () => {
    const out = formatLogLines([
      { time: "2026-01-01T12:00:00Z", priority: 3, message: "\x1b[31mboom\x1b[0m happened" },
      { time: "2026-01-01T12:00:01Z", priority: 4, message: "\x1b[1;33mcareful\x1b[0m" },
      { time: "2026-01-01T12:00:02Z", priority: 6, message: "started" },
    ]);
    const rows = out.split("\n");
    expect(rows).toHaveLength(3);
    expect(rows[0]).toContain(" ERROR boom happened");
    expect(rows[1]).toContain(" WARN careful");
    expect(rows[2]).toContain(" INFO started");
    expect(out).not.toContain("\x1b");
  });

  it("maps syslog priorities to level words", () => {
    expect(levelWord(2)).toBe("ERROR");
    expect(levelWord(4)).toBe("WARN");
    expect(levelWord(7)).toBe("DEBUG");
    expect(levelWord(5)).toBe("INFO");
  });
});
