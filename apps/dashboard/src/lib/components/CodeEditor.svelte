<script lang="ts">
  import { untrack } from "svelte";
  import { basicSetup } from "codemirror";
  import { EditorState, type Extension } from "@codemirror/state";
  import { EditorView, keymap } from "@codemirror/view";
  import { indentWithTab } from "@codemirror/commands";
  import { bareTheme, highlight, language, theme, type FileKey } from "#lib/editor.ts";

  interface Props {
    /** The text to show. A change from outside (load, apply) replaces the document. */
    value: string;
    file: FileKey;
    /** The file's name, for the editor's accessible name. */
    label: string;
    onChange: (value: string) => void;
    /** Inside a workbench: no border of its own, on the page's surface. */
    bare?: boolean;
    /** More editor extensions (language help), read once when the editor is made. */
    extensions?: Extension[];
  }
  let { value, file, label, onChange, bare = false, extensions = [] }: Props = $props();

  let host: HTMLDivElement;
  let view: EditorView | undefined;

  // CodeMirror is an imperative widget: it is made once per file and told
  // about outside changes, while typing flows out through onChange.
  $effect(() => {
    const v = new EditorView({
      parent: host,
      state: EditorState.create({
        doc: untrack(() => value),
        extensions: [
          basicSetup,
          keymap.of([indentWithTab]),
          language(file),
          theme,
          ...(untrack(() => bare) ? [bareTheme] : []),
          ...untrack(() => extensions),
          highlight,
          EditorView.contentAttributes.of({ "aria-label": label }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) onChange(u.state.doc.toString());
          }),
        ],
      }),
    });
    view = v;
    return () => {
      v.destroy();
      view = undefined;
    };
  });

  $effect(() => {
    const next = value;
    if (view && view.state.doc.toString() !== next) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: next } });
    }
  });
</script>

<div bind:this={host} class="h-full min-h-0 overflow-hidden {bare ? '' : 'rounded-md border border-line'}"></div>
