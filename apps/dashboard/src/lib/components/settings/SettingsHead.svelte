<script lang="ts">
  import type { Snippet } from "svelte";
  import Help from "#lib/components/Help.svelte";

  interface Props {
    /** The page's name, as on its tile. */
    title: string;
    /** The heading's help. */
    help: Snippet;
    /** Optional status pill shown beside the title. */
    status?: Snippet;
    /** Optional right-aligned state pill and page menu. */
    actions?: Snippet;
  }
  let { title, help, status, actions }: Props = $props();
</script>

<!-- A Settings page's head: the way back to the grid, and the heading with its help. -->
<svelte:head><title>{title} · Settings · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/settings">Settings</a> /</p>
<div class="mt-1 flex max-w-4xl flex-wrap items-start justify-between gap-x-3 gap-y-2">
  <div class="flex min-w-0 flex-1 flex-wrap items-center gap-3">
    <h1 class="text-xl font-semibold"><Help label={title}>{@render help()}</Help></h1>
    {@render status?.()}
  </div>
  {#if actions}<div class="flex flex-wrap items-center gap-2">{@render actions()}</div>{/if}
</div>
