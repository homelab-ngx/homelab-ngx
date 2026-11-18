import { describe, expect, it } from "vitest";
import { ProblemError, type Stack } from "./api/client.ts";
import { generateKey, groupVariables, missingVariables, NAME_RE, nameProblem, needsValue, variableError, type StackVariable } from "./variables.ts";

const v = (name: string, over: Partial<StackVariable> = {}): StackVariable => ({
  name,
  description: "",
  declared: false,
  secret: false,
  set: true,
  in_env: true,
  kind: "env",
  required: false,
  source: "stored",
  ...over,
});

describe("nameProblem", () => {
  it("accepts the daemon's names", () => {
    for (const n of ["A", "VPN_WG_PRIVATE_KEY", "X9_", "A".repeat(64)]) expect(nameProblem(n)).toBe("");
  });
  it("says nothing for an empty field", () => expect(nameProblem("")).toBe(""));
  it("refuses the rest with a reason", () => {
    expect(nameProblem("vpn_key")).toMatch(/capital/);
    expect(nameProblem("_A")).toMatch(/capital/);
    expect(nameProblem("9A")).toMatch(/capital/);
    expect(nameProblem("A-B")).toMatch(/A-Z/);
    expect(nameProblem("Ab")).toMatch(/A-Z/);
    expect(nameProblem("A".repeat(65))).toMatch(/64/);
  });
});

describe("generateKey", () => {
  it("gives 32 random bytes as hex", () => {
    const k = generateKey();
    expect(k).toMatch(/^[0-9a-f]{64}$/);
    expect(generateKey()).not.toBe(k);
  });
});

describe("missingVariables", () => {
  it("names the required variables without a stored value or a default", () => {
    const s = {
      variables: [
        v("MEDIA_DIR", { declared: true, kind: "path", required: true, set: false, source: "default", default: "/pool/media" }),
        v("KEY", { declared: true, kind: "secret", secret: true, required: true, set: false, in_env: false, source: "unset" }),
        v("TZ", { declared: true, required: false, set: false, source: "unset" }),
        v("DIR", { declared: true, kind: "path", required: true, set: false, source: "unset" }),
      ],
    } as unknown as Stack;
    expect(missingVariables(s)).toEqual(["KEY", "DIR"]);
    expect(needsValue(s.variables[0]!)).toBe(false);
    expect(needsValue(s.variables[1]!)).toBe(true);
  });
  it("is empty when everything required resolves", () => {
    expect(missingVariables({ variables: [v("OWN")] } as unknown as Stack)).toEqual([]);
  });
});

describe("groupVariables", () => {
  it("groups declared ones by kind and keeps the operator's own apart", () => {
    const g = groupVariables([
      v("A", { declared: true, kind: "secret", secret: true }),
      v("P", { declared: true, kind: "path" }),
      v("B"),
      v("E", { declared: true }),
      v("C", { kind: "secret", secret: true }),
      v("Q", { declared: true, kind: "path" }),
      v("H", { declared: true, kind: "address" }),
    ]);
    expect(g.path.map((x) => x.name)).toEqual(["P", "Q"]);
    expect(g.address.map((x) => x.name)).toEqual(["H"]);
    expect(g.env.map((x) => x.name)).toEqual(["E"]);
    expect(g.secret.map((x) => x.name)).toEqual(["A"]);
    expect(g.own.map((x) => x.name)).toEqual(["B", "C"]);
  });
  it("matches NAME_RE to the daemon's pattern", () => expect(NAME_RE.source).toBe("^[A-Z][A-Z0-9_]{0,63}$"));
});

describe("variableError", () => {
  it("words the daemon's refusals for the row", () => {
    const path = v("MEDIA_DIR", { kind: "path" });
    const bad = new ProblemError(422, "invalid_path", 'MEDIA_DIR: "/nope" is not on the host: stat /nope: no such file or directory');
    expect(variableError(bad, path)).toBe(
      '"/nope" is not on the host: stat /nope: no such file or directory. Use an existing directory on the host.',
    );
    const kind = new ProblemError(422, "kind_mismatch", "TZ cannot be made secret: the stack's manifest declares it as env");
    expect(variableError(kind, v("TZ"))).toBe("Declared as a setting; it cannot be made secret.");
    expect(variableError(new ProblemError(409, "other", "busy"), path)).toBe("busy");
    expect(variableError(new Error("down"), path)).toBe("down");
  });
});
