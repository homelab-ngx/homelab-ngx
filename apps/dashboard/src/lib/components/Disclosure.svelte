<script lang="ts">
  import { ChevronRight } from "@lucide/svelte";
  import type { Snippet } from "svelte";
  import { help as helpState } from "#lib/help.svelte.ts";

  interface Props {
    /** What is inside; it stays in place open or closed. */
    label: string;
    /** One line that says what the fold holds, shown beside the label. */
    summary?: string;
    open?: boolean;
    /** A key to remember on this device that the fold was opened or closed. */
    remember?: string;
    /** A section of the page: the label is set as a title. */
    section?: boolean;
    /** A bordered box, for a fold that is a section of a form. */
    boxed?: boolean;
    class?: string;
    /** Shown at the top of the fold while the header's Help switch is on. */
    help?: Snippet;
    children: Snippet;
  }
  let { label, summary, open = $bindable(false), remember, section = false, boxed = false, class: cls = "", help, children }: Props = $props();

  const KEY = "homelab-fold:";
  $effect.pre(() => {
    if (!remember) return;
    try {
      const v = localStorage.getItem(KEY + remember);
      if (v !== null) open = v === "1";
    } catch {
      // Private mode: the fold starts as the page set it.
    }
  });

  function ontoggle(e: Event) {
    open = (e.currentTarget as HTMLDetailsElement).open;
    if (!remember) return;
    try {
      localStorage.setItem(KEY + remember, open ? "1" : "0");
    } catch {
      // Not remembered, still usable.
    }
  }
</script>

<!-- The one way to fold content. -->
<details {open} {ontoggle} class="group {boxed ? 'rounded-lg border border-line px-3 py-2' : ''} {cls}">
  <summary
    class="flex cursor-pointer list-none items-center gap-1.5 rounded-sm focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent [&::-webkit-details-marker]:hidden {section
      ? ''
      : 'text-muted hover:text-fg'}"
  >
    <ChevronRight class="size-3.5 shrink-0 text-muted transition-transform group-open:rotate-90 motion-reduce:transition-none" aria-hidden="true" />
    <span class={section ? "font-semibold" : ""}>{label}</span>
    {#if summary}<span class="min-w-0 truncate text-muted {section ? 'ml-2' : ''}">{summary}</span>{/if}
  </summary>
  <div class="mt-2">
    {#if help && helpState.all}
      <p class="mb-2 border-l-2 border-accent/50 pl-3 text-muted">{@render help()}</p>
    {/if}
    {@render children()}
  </div>
</details>
