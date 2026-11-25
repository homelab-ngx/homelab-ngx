<script lang="ts">
  import type { Snippet } from "svelte";
  import { help } from "#lib/help.svelte.ts";

  interface Props {
    /** The label on the page; clicking it unfolds the help under it. */
    label: string;
    class?: string;
    children: Snippet;
  }
  let { label, class: cls = "", children }: Props = $props();

  const id = $props.id();
  /** This label's own click, on top of the header's Help switch. */
  let flipped = $state(false);
  const open = $derived(help.all !== flipped);
</script>

<!-- The page stays bare: an explanation shows only when its label is clicked
     or the header's Help switch is on. The text is a span
     so the label can sit in a heading; it takes a full row in a flex parent. -->
<button
  type="button"
  class="cursor-help rounded-sm text-left underline decoration-muted/45 decoration-dotted underline-offset-4 hover:decoration-muted focus-visible:decoration-muted focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent [letter-spacing:inherit] [text-transform:inherit] {cls}"
  aria-expanded={open}
  aria-controls={open ? id : undefined}
  onclick={() => (flipped = !flipped)}>{label}</button
>
{#if open}
  <span {id} class="order-1 col-span-full mt-1 mb-1 block w-full basis-full border-l-2 border-accent/50 pl-3 text-sm font-normal tracking-normal text-muted normal-case">
    {@render children()}
  </span>
{/if}
