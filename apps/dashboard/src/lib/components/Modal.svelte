<script lang="ts">
  import { Dialog } from "bits-ui";
  import type { Snippet } from "svelte";

  interface Props {
    open: boolean;
    /** Called when the dialog asks to open or close (Close, Escape, overlay). */
    onOpenChange: (open: boolean) => void;
    title: string;
    description?: string;
    wide?: boolean;
    /** No Close button, and Escape or a click outside does nothing: the dialog ends only through its own action. */
    locked?: boolean;
    children: Snippet;
    actions?: Snippet;
  }
  let { open, onOpenChange, title, description, wide = false, locked = false, children, actions }: Props = $props();
</script>

<Dialog.Root {open} {onOpenChange}>
  <Dialog.Portal>
    <Dialog.Overlay class="fixed inset-0 z-40 bg-black/50" />
    <Dialog.Content
      escapeKeydownBehavior={locked ? "ignore" : "close"}
      interactOutsideBehavior={locked ? "ignore" : "close"}
      class="fixed top-1/2 left-1/2 z-50 flex max-h-[85vh] w-[calc(100vw-2rem)] -translate-x-1/2 -translate-y-1/2 flex-col gap-4 rounded-lg border border-line bg-surface p-5 shadow-xl {wide ? 'max-w-4xl' : 'max-w-lg'}"
    >
      <div>
        <Dialog.Title class="text-base font-semibold">{title}</Dialog.Title>
        {#if description}
          <Dialog.Description class="mt-1 text-muted">{description}</Dialog.Description>
        {/if}
      </div>
      <div class="min-h-0 overflow-auto">{@render children()}</div>
      <div class="flex flex-wrap justify-end gap-2">
        {#if !locked}<Dialog.Close class="btn">Close</Dialog.Close>{/if}
        {@render actions?.()}
      </div>
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
