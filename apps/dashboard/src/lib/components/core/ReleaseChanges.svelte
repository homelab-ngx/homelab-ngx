<script lang="ts">
  import { areaLabel, type ReleaseNote } from "./changelog.ts";

  interface Props {
    note: ReleaseNote;
    maxItems?: number;
  }
  let { note, maxItems }: Props = $props();

  const items = $derived(maxItems && maxItems > 0 ? note.changes.slice(0, maxItems) : note.changes);
  const extra = $derived((note.more ?? 0) + Math.max(0, note.changes.length - items.length));
</script>

{#if items.length > 0}
  <ul class="grid gap-1.5">
    {#each items as item, i (`${i}-${item.area}-${item.text}`)}
      <li class="flex flex-col gap-0.5 sm:flex-row sm:items-baseline sm:gap-2.5">
        <span class="label w-24 shrink-0">{areaLabel(item.area)}</span>
        <span class="min-w-0 text-fg">{item.text}</span>
      </li>
    {/each}
  </ul>
{:else}
  <p class="text-xs text-muted">Maintenance release.</p>
{/if}

{#if extra > 0 || note.url}
  <p class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted">
    {#if extra > 0}<span>+{extra} more</span>{/if}
    {#if note.url}
      <a class="link" href={note.url} target="_blank" rel="noreferrer">Full release notes</a>
    {/if}
  </p>
{/if}
