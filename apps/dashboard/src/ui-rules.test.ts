// @vitest-environment node
/**
 * The mechanical half of DESIGN.md's "Copy and type" rules, checked over every
 * .svelte file. A failure here means a page grew text or type styles past what
 * the design allows: cut the text or move it into a HelpTip. The limits are
 * part of the design; raise one only when the operator asks for it.
 *
 * `COPY_REPORT=1 make dashboard-test FILE=src/ui-rules.test.ts` prints the
 * words each page shows; add `COPY_LIST=stacks/add` to list a page's phrases.
 */
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { parse } from "svelte/compiler";
import { describe, expect, it } from "vitest";

/** The most words one piece of fixed text may have outside help. */
const MAX_WORDS = 16;
/** A piece of text this long is a phrase or a sentence, not a label. */
const PHRASE = 4;
/** The most words in phrases and sentences a page shows with nothing open (no dialog, no help). */
const PAGE_BUDGET = 120;
/** The most words in phrases and sentences one dialog or form holds. */
const DIALOG_BUDGET = 90;
/** Mono is for logs and the stack editor: a pre element, or one of these files. */
const MONO_FILES = /(LogViewer|CodeEditor|DiffView|FileList)\.svelte$/;
const SRC = resolve(import.meta.dirname);

interface Run {
  words: number;
  text: string;
  line: number;
}
interface Scan {
  file: string;
  /** Fixed text on show, HelpTip content left out. */
  runs: Run[];
  /** Fixed text inside a Modal in this file. */
  dialogRuns: Run[];
  /** Text built in the script: messages of which one shows at a time. Capped per piece, left out of the budgets. */
  messages: Run[];
  /** Mono outside a pre element. */
  mono: Run[];
  /** Font sizes written as arbitrary values, and raw details elements. */
  sizes: Run[];
  details: number[];
  imports: string[];
}

type Node = { type?: string; start?: number; [k: string]: unknown };

function files(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name);
    if (e.isDirectory()) return files(p);
    return e.name.endsWith(".svelte") && !e.name.endsWith(".test.svelte") ? [p] : [];
  });
}

const tokens = (s: string) => s.split(/\s+/).filter((t) => /[A-Za-z]/.test(t));
/** Prose, as opposed to a class list, a key or a format: mostly plain words. */
function prose(s: string): boolean {
  const t = tokens(s);
  if (t.length < 3) return false;
  return t.filter((w) => /^[("']?[A-Za-z][A-Za-z'’]*[)"'.,:;!?…]*$/.test(w)).length / t.length >= 0.7;
}

/** String literals in an expression or a script, joined per literal. */
function literals(node: unknown, out: { text: string; start: number }[]) {
  if (!node || typeof node !== "object") return;
  if (Array.isArray(node)) return node.forEach((n) => literals(n, out));
  const n = node as Node;
  if (n.type === "ImportDeclaration" || n.type === "TSTypeAnnotation" || n.type === "TSInterfaceDeclaration" || n.type === "TSTypeAliasDeclaration") return;
  if (n.type === "Literal" && typeof n.value === "string") out.push({ text: n.value, start: n.start ?? 0 });
  else if (n.type === "TemplateLiteral") {
    const quasis = (n.quasis as { value: { cooked: string } }[]).map((q) => q.value.cooked).join(" x ");
    out.push({ text: quasis, start: n.start ?? 0 });
    literals(n.expressions, out);
  } else for (const [k, v] of Object.entries(n)) if (k !== "loc" && k !== "leadingComments" && k !== "trailingComments") literals(v, out);
}

function scan(path: string): Scan {
  const source = readFileSync(path, "utf8");
  const ast = parse(source, { modern: true }) as unknown as { fragment: Node; instance?: { content: Node }; module?: { content: Node } };
  const line = (pos: number) => source.slice(0, pos).split("\n").length;
  const s: Scan = { file: relative(SRC, path), runs: [], dialogRuns: [], messages: [], mono: [], sizes: [], details: [], imports: [] };

  for (const m of source.matchAll(/from\s+"([^"]+\.svelte)"/g)) {
    const spec = m[1]!;
    s.imports.push(spec.startsWith("#lib/") ? join(SRC, "lib", spec.slice(5)) : resolve(dirname(path), spec));
  }

  interface Ctx {
    help: boolean;
    dialog: boolean;
  }
  const add = (text: string, start: number, ctx: Ctx) => {
    const words = tokens(text).length;
    if (!words) return;
    const run = { words, text: text.trim().replace(/\s+/g, " "), line: line(start) };
    if (ctx.help) return;
    (ctx.dialog ? s.dialogRuns : s.runs).push(run);
  };

  const classOf = (n: Node, record = false): string => {
    const attrs = (n.attributes as Node[] | undefined) ?? [];
    const out: { text: string; start: number }[] = [];
    for (const a of attrs) {
      if (a.type !== "Attribute" || a.name !== "class") continue;
      const v = a.value;
      for (const part of Array.isArray(v) ? (v as Node[]) : v && typeof v === "object" ? [v as Node] : []) {
        if (part.type === "Text") out.push({ text: String(part.data), start: part.start ?? 0 });
        else literals(part.expression, out);
      }
    }
    if (record) for (const o of out) for (const m of o.text.matchAll(/text-\[[^\]]+\]/g)) s.sizes.push({ words: 0, text: m[0], line: line(o.start) });
    return out.map((o) => o.text).join(" ");
  };

  const walk = (n: Node, ctx: Ctx) => {
    if (!n || typeof n !== "object") return;
    switch (n.type) {
      case "Text":
        return add(String(n.data), n.start ?? 0, ctx);
      case "ExpressionTag": {
        const out: { text: string; start: number }[] = [];
        literals(n.expression, out);
        for (const o of out) if (prose(o.text)) add(o.text, o.start, ctx);
        return;
      }
      case "SvelteHead":
      case "Comment":
        return;
    }
    let next = ctx;
    // A snippet named help... is help text: a fold's help, or what a label unfolds.
    if (n.type === "SnippetBlock" && String((n.expression as Node | undefined)?.name).startsWith("help")) next = { ...next, help: true };
    if (n.type === "RegularElement" || n.type === "Component" || n.type === "SvelteElement") {
      const cls = classOf(n, true);
      if (n.name === "details") s.details.push(line(n.start ?? 0));
      if (n.name === "option") return;
      if (n.name === "Help") next = { ...next, help: true };
      if (typeof n.name === "string" && /(^Modal$|Dialog)/.test(n.name)) next = { ...next, dialog: true };
      if (/(^|\s)font-mono(\s|$)/.test(cls) && n.name !== "pre") s.mono.push({ words: 0, text: `<${String(n.name)}>`, line: line(n.start ?? 0) });
      for (const a of (n.attributes as Node[] | undefined) ?? []) {
        if (a.type !== "Attribute" || (a.name !== "description" && a.name !== "title") || n.type !== "Component") continue;
        const v = a.value;
        const parts = Array.isArray(v) ? (v as Node[]) : v && typeof v === "object" ? [v as Node] : [];
        const text = parts.map((p) => (p.type === "Text" ? String(p.data) : " x ")).join("");
        if (a.name === "description") add(text, a.start ?? 0, next);
      }
    }
    for (const [k, v] of Object.entries(n)) {
      if (k === "attributes" || k === "expression" || k === "test" || k === "context" || k === "key") continue;
      if (Array.isArray(v)) v.forEach((c) => walk(c as Node, next));
      else if (v && typeof v === "object" && "type" in v) walk(v as Node, next);
    }
  };
  walk(ast.fragment, { help: false, dialog: false });

  for (const script of [ast.instance, ast.module]) {
    if (!script) continue;
    const out: { text: string; start: number }[] = [];
    literals(script.content, out);
    for (const o of out) if (prose(o.text)) s.messages.push({ words: tokens(o.text).length, text: o.text.trim().replace(/\s+/g, " "), line: line(o.start) });
  }
  return s;
}

const scans = new Map(files(SRC).map((f) => [f, scan(f)]));
const all = scans;
const sum = (runs: Run[]) => runs.reduce((n, r) => n + (r.words >= PHRASE ? r.words : 0), 0);
const isDialog = (f: string) => /(Dialog|Form)\.svelte$/.test(f);

/** A page's own files: the route and the components it shows inline, dialogs and forms left out. */
function closure(route: string): string[] {
  const seen = new Set<string>();
  const visit = (f: string) => {
    if (seen.has(f) || !scans.has(f)) return;
    seen.add(f);
    for (const i of scans.get(f)!.imports) if (!isDialog(i) && !/\/(Shell|Modal)\.svelte$/.test(i)) visit(i);
  };
  visit(route);
  return [...seen];
}

const routes = [...scans.keys()].filter((f) => f.endsWith("+page.svelte"));
const at = (s: Scan, r: Run) => `${s.file}:${r.line} (${r.words} words) ${r.text.slice(0, 90)}`;

describe("copy and type rules (DESIGN.md)", () => {
  if (process.env.COPY_REPORT) {
    const rows = routes.map((r) => ({ page: relative(SRC, r), words: sum(closure(r).flatMap((f) => scans.get(f)!.runs)) }));
    for (const [f, s] of scans) if (isDialog(f) || s.dialogRuns.length) rows.push({ page: `dialog ${s.file}`, words: sum([...s.dialogRuns, ...(isDialog(f) ? s.runs : [])]) });
    console.table(rows.sort((a, b) => b.words - a.words));
    console.log("total words on show", sum([...scans.values()].flatMap((s) => [...s.runs, ...s.dialogRuns])));
  }

  // COPY_LIST=<part of a route's path> prints every phrase that counts toward that page.
  if (process.env.COPY_LIST) {
    for (const r of routes.filter((r) => r.includes(process.env.COPY_LIST!)))
      for (const f of closure(r)) for (const run of all.get(f)!.runs.filter((x) => x.words >= PHRASE)) console.log(at(all.get(f)!, run));
  }

  it(`no fixed text runs past ${MAX_WORDS} words outside help`, () => {
    const long = [...scans.values()].flatMap((s) => [...s.runs, ...s.dialogRuns, ...s.messages].filter((r) => r.words > MAX_WORDS).map((r) => at(s, r)));
    expect(long, "shorten these, or move the explanation into Help").toEqual([]);
  });

  it(`a page shows at most ${PAGE_BUDGET} words of phrases and sentences`, () => {
    const over = routes
      .map((r) => ({ page: relative(SRC, r), words: sum(closure(r).flatMap((f) => scans.get(f)!.runs)) }))
      .filter((p) => p.words > PAGE_BUDGET)
      .map((p) => `${p.page}: ${p.words} words`);
    expect(over, "cut text on these pages; help or a dialog holds what is rarely needed").toEqual([]);
  });

  it(`a dialog or form holds at most ${DIALOG_BUDGET} words of phrases and sentences`, () => {
    const over = [...scans.entries()]
      .map(([f, s]) => ({ file: s.file, words: sum([...s.dialogRuns, ...(isDialog(f) ? s.runs : [])]) }))
      .filter((d) => d.words > DIALOG_BUDGET)
      .map((d) => `${d.file}: ${d.words} words`);
    expect(over).toEqual([]);
  });

  it("font sizes come from the scale, not arbitrary values", () => {
    const found = [...scans.values()].flatMap((s) => s.sizes.map((r) => `${s.file}:${r.line} ${r.text}`));
    expect(found, "use the body size, text-xs or .label (see DESIGN.md, Typography)").toEqual([]);
  });

  it("mono is for logs and the stack editor only", () => {
    const found = [...scans.values()].filter((s) => !MONO_FILES.test(s.file)).flatMap((s) => s.mono.map((r) => `${s.file}:${r.line} ${r.text}`));
    expect(found, "set values in the body font; mono belongs on a pre element").toEqual([]);
  });

  it("things fold with Disclosure, not a raw details element", () => {
    const found = [...scans.values()].filter((s) => !s.file.endsWith("Disclosure.svelte")).flatMap((s) => s.details.map((l) => `${s.file}:${l}`));
    expect(found).toEqual([]);
  });
});
