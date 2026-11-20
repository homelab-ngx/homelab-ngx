<script lang="ts">
  import type { Schema } from "#lib/api/client.ts";
  import { bytes } from "#lib/format.ts";
  import { kindWord } from "#lib/storage.ts";

  interface Props {
    disk: Schema<"Disk">;
  }
  let { disk }: Props = $props();
</script>

<!-- One spare disk in a pool dialog's list: what it is, and what a pool would erase. -->
<span class="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-3 gap-y-0.5">
  <span>{disk.name}</span>
  <span class="min-w-0 text-muted [overflow-wrap:anywhere]">{disk.friendly_name || disk.model}</span>
  <span class="ml-auto whitespace-nowrap text-muted tabular-nums">{kindWord(disk)} · {bytes(disk.size)}</span>
  {#if disk.contents}<span class="basis-full text-xs text-warn">Holds {disk.contents}</span>{/if}
</span>
