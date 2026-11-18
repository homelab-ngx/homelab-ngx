/**
 * CodeMirror setup for the stack editor: the dashboard's tokens as the
 * theme (so light and dark follow the page), and a language per file.
 */
import { json } from "@codemirror/lang-json";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import type { Extension } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { tags as t } from "@lezer/highlight";
import { nix } from "@replit/codemirror-lang-nix";

export type FileKey = "manifest" | "flake";

/** The two files of a stack, as they are named in its repo. */
export const FILES: { key: FileKey; path: string }[] = [
  { key: "manifest", path: "homelab-stack.json" },
  { key: "flake", path: "flake.nix" },
];

export const language = (key: FileKey): Extension => (key === "manifest" ? json() : nix());

/** The editor in the stack editor's workbench: white like the page, no focus ring of its own. */
export const bareTheme = EditorView.theme({
  "&": { backgroundColor: "var(--surface)" },
  "&.cm-focused": { outline: "none" },
  ".cm-gutters": { backgroundColor: "var(--surface)" },
});

export const theme = EditorView.theme({
  "&": {
    color: "var(--fg)",
    backgroundColor: "var(--sunken)",
    fontSize: "13px",
    height: "100%",
  },
  "&.cm-focused": { outline: "2px solid var(--accent)", outlineOffset: "-2px" },
  ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.55" },
  ".cm-content": { caretColor: "var(--accent)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--accent)" },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection":
    { backgroundColor: "var(--accent-soft)" },
  ".cm-gutters": { backgroundColor: "var(--sunken)", color: "var(--muted)", border: "none", borderRight: "1px solid var(--line)" },
  ".cm-activeLine": { backgroundColor: "color-mix(in srgb, var(--accent-soft) 35%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--fg)" },
  ".cm-matchingBracket": { backgroundColor: "var(--accent-soft)", outline: "none" },
  ".cm-searchMatch": { backgroundColor: "var(--warn-soft)" },
  ".cm-panels": { backgroundColor: "var(--surface)", color: "var(--fg)", borderColor: "var(--line)" },
  ".cm-tooltip": { backgroundColor: "var(--surface)", color: "var(--fg)", border: "1px solid var(--line)", borderRadius: "6px" },
  // Hover docs and completion details: body text, not mono, kept narrow.
  ".cm-schema-doc, .cm-lsp-documentation, .cm-completionInfo, .cm-tooltip-lint": {
    fontFamily: "var(--font-sans)",
    fontSize: "13px",
    lineHeight: "1.5",
    maxWidth: "28rem",
  },
  ".cm-schema-doc, .cm-lsp-documentation": { padding: "6px 10px" },
  ".cm-lsp-documentation pre, .cm-lsp-documentation code": { fontFamily: "var(--font-mono)", fontSize: "12px" },
  ".cm-tooltip.cm-tooltip-autocomplete > ul": { fontFamily: "var(--font-mono)", fontSize: "12px" },
  ".cm-tooltip.cm-tooltip-autocomplete > ul > li[aria-selected]": { backgroundColor: "var(--accent-soft)", color: "var(--fg)" },
  ".cm-diagnostic": { padding: "4px 8px" },
  ".cm-diagnostic-error": { borderLeft: "3px solid var(--bad)" },
  ".cm-diagnostic-warning": { borderLeft: "3px solid var(--warn)" },
  ".cm-lintRange-error": { backgroundImage: "none", textDecoration: "underline wavy var(--bad)", textUnderlineOffset: "3px" },
  ".cm-lintRange-warning": { backgroundImage: "none", textDecoration: "underline wavy var(--warn)", textUnderlineOffset: "3px" },
  // The merge view's changes. Its "unchanged lines" marker uses symbols the
  // mono font lacks.
  ".cm-collapsedLines": { fontFamily: "var(--font-sans)", fontSize: "12px", color: "var(--muted)", background: "var(--surface)" },
  ".cm-changedLine": { backgroundColor: "color-mix(in srgb, var(--ok-soft) 70%, transparent)" },
  ".cm-changedText": { background: "color-mix(in srgb, var(--ok) 25%, transparent)" },
  ".cm-deletedChunk": { backgroundColor: "color-mix(in srgb, var(--bad-soft) 80%, transparent)" },
  ".cm-deletedChunk .cm-deletedText, del": { background: "color-mix(in srgb, var(--bad) 25%, transparent)" },
});

export const highlight = syntaxHighlighting(
  HighlightStyle.define([
    { tag: [t.comment, t.lineComment, t.blockComment], color: "var(--muted)", fontStyle: "italic" },
    { tag: [t.string, t.special(t.string)], color: "var(--ok)" },
    { tag: [t.number, t.bool, t.null, t.atom], color: "var(--warn)" },
    { tag: [t.keyword, t.operatorKeyword, t.controlKeyword, t.definitionKeyword], color: "var(--core)" },
    { tag: [t.propertyName, t.attributeName], color: "var(--accent)" },
    { tag: [t.variableName, t.function(t.variableName)], color: "var(--fg)" },
    { tag: t.invalid, color: "var(--bad)" },
  ]),
);
