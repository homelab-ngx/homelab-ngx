/**
 * Editor help for homelab-stack.json: completion and hover from the
 * manifest's JSON Schema (GET /stacks-manifest-schema), and marks from the
 * JSON parser and the daemon's own Check, placed on the field each problem
 * names. Check stays the one judge of a manifest; the schema only explains.
 */
import { autocompletion, type Completion, type CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { jsonParseLinter } from "@codemirror/lang-json";
import { syntaxTree } from "@codemirror/language";
import { type Diagnostic, linter, lintGutter } from "@codemirror/lint";
import type { EditorState, Extension } from "@codemirror/state";
import { hoverTooltip } from "@codemirror/view";

type SyntaxNode = ReturnType<typeof syntaxTree>["topNode"];

/** A JSON Schema node, as far as the editor reads one. */
export interface SchemaNode {
  description?: string;
  type?: string;
  const?: unknown;
  enum?: unknown[];
  examples?: unknown[];
  properties?: Record<string, SchemaNode>;
  items?: SchemaNode;
  allOf?: SchemaNode[];
  $ref?: string;
  definitions?: Record<string, SchemaNode>;
}

type Path = (string | number)[];

/** The node at ref (#/definitions/x), with allOf and $ref folded in. */
function resolve(root: SchemaNode, node: SchemaNode | undefined): SchemaNode | undefined {
  if (!node) return undefined;
  let out: SchemaNode = { ...node };
  if (node.$ref?.startsWith("#/definitions/")) {
    const target = resolve(root, root.definitions?.[node.$ref.slice("#/definitions/".length)]);
    out = { ...target, ...out, description: node.description ?? target?.description };
  }
  for (const part of node.allOf ?? []) {
    const r = resolve(root, part);
    if (r) out = { ...r, ...out, description: out.description ?? r.description };
  }
  return out;
}

/** The schema of the value at path. */
export function schemaAt(root: SchemaNode, path: Path): SchemaNode | undefined {
  let node = resolve(root, root);
  for (const step of path) {
    if (!node) return undefined;
    node = resolve(root, typeof step === "number" ? node.items : node.properties?.[step]);
  }
  return node;
}

const unquote = (s: string) => {
  try {
    return JSON.parse(s) as string;
  } catch {
    return s.replace(/^"|"$/g, "");
  }
};

/** The path of the value a node is in: keys of the Properties and indexes in the Arrays above it. */
function pathOf(state: EditorState, node: SyntaxNode | null): Path {
  const path: Path = [];
  for (let n = node; n; n = n.parent) {
    const parent = n.parent;
    if (n.name === "Property") {
      const key = n.getChild("PropertyName");
      if (key) path.unshift(unquote(state.sliceDoc(key.from, key.to)));
    } else if (parent?.name === "Array" && !["[", "]", ","].includes(n.name)) {
      let i = 0;
      for (let s = n.prevSibling; s; s = s.prevSibling) if (!["[", "]", ","].includes(s.name)) i++;
      path.unshift(i);
    }
  }
  return path;
}

/** The node at a path, for a Check problem: the property's name, else the closest value found. */
function nodeAt(state: EditorState, path: Path): SyntaxNode | null {
  let value: SyntaxNode | null = syntaxTree(state).topNode.firstChild;
  let mark: SyntaxNode | null = value;
  for (const step of path) {
    if (!value) break;
    if (typeof step === "number" && value.name === "Array") {
      let i = 0;
      let item: SyntaxNode | null = null;
      for (let c = value.firstChild; c; c = c.nextSibling) {
        if (["[", "]", ","].includes(c.name)) continue;
        if (i++ === step) {
          item = c;
          break;
        }
      }
      value = item;
      if (value) mark = value;
    } else if (typeof step === "string" && value.name === "Object") {
      const prop = value.getChildren("Property").find((p) => {
        const k = p.getChild("PropertyName");
        return k && unquote(state.sliceDoc(k.from, k.to)) === step;
      });
      if (!prop) break;
      mark = prop.getChild("PropertyName");
      value = prop.lastChild;
    } else break;
  }
  return mark;
}

/** "routes[0].port: …" → ["routes", 0, "port"] and the message. */
export function problemPath(message: string): { path: Path; text: string } {
  const m = /^([a-z_]+(?:\[\d+\]|\.[a-z_]+)*):\s+(.*)$/s.exec(message);
  if (!m || m[1] === "decode") return { path: [], text: message };
  if (m[1] === "version") return { path: ["version"], text: message };
  const path: Path = [];
  for (const part of m[1]!.matchAll(/([a-z_]+)|\[(\d+)\]/g)) path.push(part[2] !== undefined ? Number(part[2]) : part[1]!);
  return { path, text: m[2]! };
}

/** The innermost object around pos, even inside half-typed JSON. */
function objectAt(state: EditorState, pos: number): SyntaxNode | null {
  for (let n: SyntaxNode | null = syntaxTree(state).resolveInner(pos, -1); n; n = n.parent) {
    if (n.name === "Object" && n.from < pos) return n;
  }
  return null;
}

/** Where the object's current entry starts: after its last "{" or "," before pos. */
function entryStart(obj: SyntaxNode, pos: number): number {
  let start = obj.from + 1;
  for (let c = obj.firstChild; c && c.from < pos; c = c.nextSibling) {
    if (c.name === "," || c.name === "{") start = c.to;
    else if (c.name === "Property" && c.to <= pos) start = c.to;
  }
  return start;
}

/** Completion: a key in an object (its schema's properties not yet there), or a value (enum, const, examples, booleans). */
export function completeAt(root: SchemaNode, state: EditorState, pos: number): CompletionResult | null {
  const obj = objectAt(state, pos);
  if (!obj) return null;
  const before = state.sliceDoc(entryStart(obj, pos), pos);
  const objPath = pathOf(state, obj);
  const colon = /"((?:[^"\\]|\\.)*)"\s*:\s*("?)([^",{}[\]]*)$/.exec(before);
  if (!colon) {
    // A key. The word typed so far is matched against the labels; applying one
    // replaces the whole key, its quotes included (the closing one may be
    // there already, put in by the editor).
    const word = /[A-Za-z_]*$/.exec(before)![0];
    const quoted = before.slice(0, before.length - word.length).endsWith('"');
    const keyFrom = pos - word.length - (quoted ? 1 : 0);
    const s = schemaAt(root, objPath);
    if (!s?.properties) return null;
    const taken = new Set(
      obj.getChildren("Property").flatMap((p) => {
        const k = p.getChild("PropertyName");
        return k && !(pos >= p.from && pos <= p.to) ? [unquote(state.sliceDoc(k.from, k.to))] : [];
      }),
    );
    const options: Completion[] = Object.entries(s.properties)
      .filter(([k]) => !taken.has(k))
      .map(([k, v]) => ({
        label: k,
        type: "property",
        info: resolve(root, v)?.description,
        apply: (view, _c, _from, to) => {
          const end = view.state.sliceDoc(to, to + 1) === '"' ? to + 1 : to;
          view.dispatch({ changes: { from: keyFrom, to: end, insert: `"${k}": ` }, selection: { anchor: keyFrom + k.length + 4 } });
        },
      }));
    return options.length ? { from: pos - word.length, options, validFor: /^[A-Za-z_]*$/ } : null;
  }
  const s = schemaAt(root, [...objPath, colon[1]!]);
  const values = s?.const !== undefined ? [s.const] : (s?.enum ?? s?.examples ?? (s?.type === "boolean" ? [true, false] : []));
  if (!values.length) return null;
  const typed = colon[2]! + colon[3]!;
  const from = pos - typed.length;
  return {
    from,
    options: values.map((v) => ({
      label: JSON.stringify(v),
      type: "enum",
      apply: (view, c, _from, to) => {
        const end = view.state.sliceDoc(to, to + 1) === '"' && c.label.endsWith('"') ? to + 1 : to;
        view.dispatch({ changes: { from, to: end, insert: c.label }, selection: { anchor: from + c.label.length } });
      },
    })),
  };
}

function completions(schema: () => SchemaNode | undefined) {
  return (ctx: CompletionContext): CompletionResult | null => {
    const root = schema();
    return root ? completeAt(root, ctx.state, ctx.pos) : null;
  };
}

function hover(schema: () => SchemaNode | undefined) {
  return hoverTooltip((view, pos, side) => {
    const root = schema();
    const node = syntaxTree(view.state).resolveInner(pos, side);
    if (!root || node.name !== "PropertyName") return null;
    const s = schemaAt(root, pathOf(view.state, node.parent));
    if (!s?.description) return null;
    return {
      pos: node.from,
      end: node.to,
      create: () => {
        const dom = document.createElement("div");
        dom.className = "cm-schema-doc";
        dom.textContent = s.description!;
        return { dom };
      },
    };
  });
}

/**
 * The manifest editor's help. schema() is the fetched schema (undefined
 * until it arrives); check(text) runs the daemon's Check on the editor's
 * text and returns its problems for homelab-stack.json.
 */
export function manifestAssist(schema: () => SchemaNode | undefined, check: (text: string) => Promise<string[]>): Extension {
  const parse = jsonParseLinter();
  return [
    autocompletion({ override: [completions(schema)] }),
    hover(schema),
    lintGutter(),
    linter(
      async (view) => {
        const syntax = parse(view);
        if (syntax.length) return syntax;
        let messages: string[];
        try {
          messages = await check(view.state.doc.toString());
        } catch {
          return [];
        }
        return messages.map((m): Diagnostic => {
          const { path, text } = problemPath(m);
          const unknown = /unknown field "([^"]+)"/.exec(m);
          let node = nodeAt(view.state, path);
          if (unknown) {
            syntaxTree(view.state).iterate({
              enter: (n) => {
                if (n.name === "PropertyName" && unquote(view.state.sliceDoc(n.from, n.to)) === unknown[1]) node = n.node;
              },
            });
          }
          const whole = !unknown && path.length === 0;
          const from = whole ? 0 : (node?.from ?? 0);
          const to = !whole && node && node.to > from ? node.to : Math.min(view.state.doc.length, from + 1);
          return { from, to, severity: "error", message: path.length ? text : m, source: "Check" };
        });
      },
      { delay: 750 },
    ),
  ];
}
