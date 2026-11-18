import { json } from "@codemirror/lang-json";
import { ensureSyntaxTree } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { completeAt, problemPath, schemaAt, type SchemaNode } from "./manifest-assist.ts";

const schema: SchemaNode = {
  properties: {
    routes: { type: "array", items: { $ref: "#/definitions/route" } },
    gpu: { type: "boolean", description: "The GPU." },
    nixpkgs: { type: "string", examples: ["nixos-unstable"] },
    version: { const: 8 },
  },
  definitions: {
    portNumber: { type: "integer" },
    route: { properties: { port: { description: "The port in the stack.", $ref: "#/definitions/portNumber" } } },
  },
};

describe("manifest assist", () => {
  it("finds the schema of a path through refs and arrays", () => {
    expect(schemaAt(schema, ["gpu"])?.description).toBe("The GPU.");
    const port = schemaAt(schema, ["routes", 0, "port"]);
    expect(port?.description).toBe("The port in the stack.");
    expect(port?.type).toBe("integer");
    expect(schemaAt(schema, ["routes", 0, "nope"])).toBeUndefined();
  });

  it("reads the field a Check problem names", () => {
    expect(problemPath("routes[0].port: 70000 is not a port")).toEqual({ path: ["routes", 0, "port"], text: "70000 is not a port" });
    expect(problemPath("health.units[1]: \"x\" is not a .service unit name").path).toEqual(["health", "units", 1]);
    expect(problemPath("version: api needs version 3").path).toEqual(["version"]);
    expect(problemPath('decode: json: unknown field "foo"').path).toEqual([]);
  });

  const at = (doc: string) => {
    const pos = doc.indexOf("|");
    const state = EditorState.create({ doc: doc.replace("|", ""), extensions: [json()] });
    ensureSyntaxTree(state, state.doc.length, 5000);
    return completeAt(schema, state, pos);
  };
  const labels = (r: ReturnType<typeof completeAt>) => r?.options.map((o) => o.label) ?? [];

  it("completes keys, also half-typed, without the ones already there", () => {
    expect(labels(at('{\n  "version": 8,\n  "gp|\n}'))).toEqual(["routes", "gpu", "nixpkgs"]);
    expect(at('{\n  "version": 8,\n  "gp|\n}')?.from).toBe('{\n  "version": 8,\n  "'.length);
    expect(labels(at('{ "routes": [ { | } ] }'))).toEqual(["port"]);
  });

  it("completes values from const, examples and booleans", () => {
    expect(labels(at('{ "version": | }'))).toEqual(["8"]);
    expect(labels(at('{ "gpu": t| }'))).toEqual(["true", "false"]);
    expect(labels(at('{ "nixpkgs": "ni| }'))).toEqual(['"nixos-unstable"']);
  });
});
