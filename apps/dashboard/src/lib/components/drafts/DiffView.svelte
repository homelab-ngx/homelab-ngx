<script lang="ts">
  import { EditorState } from "@codemirror/state";
  import { EditorView, lineNumbers } from "@codemirror/view";
  import { unifiedMergeView } from "@codemirror/merge";
  import { highlight, language, theme, type FileKey } from "#lib/editor.ts";

  interface Props {
    file: FileKey;
    /** The editor's text now. */
    original: string;
    /** The text the assistant proposes. */
    proposed: string;
    label: string;
  }
  let { file, original, proposed, label }: Props = $props();

  let host: HTMLDivElement;

  // A read-only view of the proposal with the lines it removes shown inline;
  // made again whenever either side changes.
  $effect(() => {
    const v = new EditorView({
      parent: host,
      state: EditorState.create({
        doc: proposed,
        extensions: [
          lineNumbers(),
          EditorState.readOnly.of(true),
          EditorView.editable.of(false),
          // The panel is narrow: long lines wrap instead of hiding the change.
          EditorView.lineWrapping,
          language(file),
          theme,
          highlight,
          EditorView.contentAttributes.of({ "aria-label": label }),
          unifiedMergeView({ original, mergeControls: false, collapseUnchanged: { margin: 3, minSize: 6 } }),
        ],
      }),
    });
    return () => v.destroy();
  });
</script>

<div bind:this={host} class="max-h-[28rem] overflow-auto rounded-md border border-line"></div>
