import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// Every daemon call goes through the typed client (client.ts), so a spec
// change breaks the build instead of a page at runtime.
function files(dir: string): string[] {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    return statSync(p).isDirectory() ? files(p) : /\.(ts|svelte)$/.test(f) && !f.endsWith(".test.ts") ? [p] : [];
  });
}

describe("API access", () => {
  it("never calls fetch directly", () => {
    const raw = files("src").filter((f) => !f.endsWith("schema.gen.ts") && /\bfetch\(/.test(readFileSync(f, "utf8")));
    expect(raw).toEqual([]);
  });
});
