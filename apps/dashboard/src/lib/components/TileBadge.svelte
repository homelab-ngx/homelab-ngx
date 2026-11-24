<script lang="ts">
  import type { TileStatus } from "#lib/settings.ts";
  import StatusMark from "./StatusMark.svelte";

  /** A System Settings tile's badge, at its icon's top right; the icon's box is the positioning parent. */
  let { status }: { status: TileStatus | undefined } = $props();

  const pill = { ok: "bg-ok-soft text-ok", warn: "bg-warn-soft text-warn", bad: "bg-bad-soft text-bad", info: "bg-accent-soft text-accent", none: "border border-line bg-surface text-fg" };
</script>

<!-- The tile link's label says the same in words, so the badge is hidden from screen readers.
     A count is a small pill in the tone's colour; a tone alone is its status mark. Off shows nothing: the icon dims. -->
{#if status && status.state !== "off"}
  {#if status.count !== undefined}
    <span aria-hidden="true" class="absolute -top-1.5 left-full -ml-2 min-w-4 rounded-full px-1 text-center text-xs leading-4 font-semibold tabular-nums {pill[status.state ?? 'none']}">{status.count}</span>
  {:else if status.state}
    <span aria-hidden="true" class="absolute -top-1 -right-1.5 flex rounded-full bg-surface p-0.5 group-hover:bg-sunken"><StatusMark tone={status.state} /></span>
  {/if}
{/if}
