<script lang="ts">
  import type { Snippet } from "svelte";
  import Help from "#lib/components/Help.svelte";

  interface Props {
    /** The page's name, as on its tile. */
    title: string;
    /** The heading's help. */
    help: Snippet;
  }
  let { title, help }: Props = $props();
</script>

<!-- A Settings page's head: the way back to the grid, and the heading with its help. -->
<svelte:head><title>{title} · Settings · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/settings">Settings</a> /</p>
<h1 class="mt-1 text-xl font-semibold"><Help label={title}>{@render help()}</Help></h1>
