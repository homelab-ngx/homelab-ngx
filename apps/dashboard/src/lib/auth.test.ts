import { describe, expect, it } from "vitest";
import { MIN_PASSWORD, newPasswordProblem } from "./auth.ts";

describe("newPasswordProblem", () => {
  it("asks for the minimum length first", () => {
    expect(newPasswordProblem("short", "short")).toBe(`At least ${MIN_PASSWORD} characters.`);
  });
  it("wants the two entries to match", () => {
    expect(newPasswordProblem("correct horse", "correct horsE")).toBe("The two passwords differ.");
  });
  it("counts bytes against the ceiling, as the daemon does", () => {
    const long = "å".repeat(129); // 258 bytes
    expect(newPasswordProblem(long, long)).toBe("At most 256 characters.");
  });
  it("accepts a good pair", () => {
    expect(newPasswordProblem("correct horse", "correct horse")).toBe("");
  });
});
