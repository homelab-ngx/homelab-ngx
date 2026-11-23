<script lang="ts">
  import type { Stack } from "#lib/api/client.ts";
  import { stackTitles } from "#lib/stacks.ts";

  interface Props {
    /** The stacks that use a folder, by name. */
    names: string[];
    stacks: Stack[];
  }
  let { names, stacks }: Props = $props();
  const title = $derived(stackTitles(stacks));
</script>

<!-- Who uses a folder: one chip link per stack (DESIGN.md, Chip Link). -->
{#each names as u (u)}
  <a class="rounded border border-line bg-sunken px-1.5 text-xs hover:border-accent hover:text-accent" href="/stacks/{u}">{title(u)}</a>
{/each}
