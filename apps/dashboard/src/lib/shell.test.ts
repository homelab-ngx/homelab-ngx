import { describe, expect, it } from "vitest";
import { isCopyKey, refusal, resizeFrame, shellPath, terminalTheme } from "./shell";

const key = (code: string, mods: Partial<Record<"metaKey" | "ctrlKey" | "shiftKey" | "altKey", boolean>> = {}) => ({
  key: code.slice(3).toLowerCase(),
  code,
  metaKey: false,
  ctrlKey: false,
  shiftKey: false,
  altKey: false,
  ...mods,
});

describe("isCopyKey", () => {
  it("is Cmd+C on a Mac", () => {
    expect(isCopyKey(key("KeyC", { metaKey: true }), true)).toBe(true);
    expect(isCopyKey(key("KeyC", { ctrlKey: true }), true)).toBe(false);
  });

  it("is Ctrl+Shift+C elsewhere, and Ctrl+C stays an interrupt", () => {
    expect(isCopyKey(key("KeyC", { ctrlKey: true, shiftKey: true }), false)).toBe(true);
    expect(isCopyKey(key("KeyC", { ctrlKey: true }), false)).toBe(false);
    expect(isCopyKey(key("KeyC", { metaKey: true }), false)).toBe(false);
  });

  it("is no other key", () => {
    expect(isCopyKey(key("KeyV", { metaKey: true }), true)).toBe(false);
    expect(isCopyKey(key("KeyC", { metaKey: true, altKey: true }), true)).toBe(false);
  });
});

describe("the wire", () => {
  it("sends a resize as a text frame", () => {
    expect(JSON.parse(resizeFrame(120, 40))).toEqual({ type: "resize", cols: 120, rows: 40 });
  });

  it("opens the window at the stack's own path", () => {
    expect(shellPath("home assistant")).toBe("/shell/home%20assistant");
  });
});

describe("refusal", () => {
  it("names why a shell would not open", () => {
    expect(refusal({ running: false, frozen: false })).toBe("The stack is not running.");
    expect(refusal({ running: true, frozen: true })).toBe("The stack is frozen.");
    expect(refusal({ running: true, frozen: false })).toBeNull();
    expect(refusal(null)).toBeNull();
  });
});

describe("terminalTheme", () => {
  it("maps ANSI colours to the tokens as the log pane does", () => {
    const t = terminalTheme((name) => `#${name}`);
    expect(t.background).toBe("#sunken");
    expect(t.foreground).toBe("#fg");
    expect(t.red).toBe("#bad");
    expect(t.brightRed).toBe("#bad");
    expect(t.magenta).toBe("#core");
    expect(t.black).toBe("#muted");
  });
});
