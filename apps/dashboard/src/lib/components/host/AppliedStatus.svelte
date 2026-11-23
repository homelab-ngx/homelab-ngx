<script lang="ts">
  import Pill from "#lib/components/Pill.svelte";
  import { stamp } from "#lib/format.ts";
  import type { HostSettings } from "#lib/host.ts";

  /** Who keeps the host's settings, and how their last apply went. */
  let { settings: s }: { settings: HostSettings } = $props();
</script>

<div class="mt-1 flex flex-wrap items-center gap-2 text-muted">
  {#if s.managed === "nixos"}
    <Pill tone="info">nixos</Pill><span>Set in this host's NixOS configuration.</span>
  {:else if s.applied.errors.length > 0}
    <Pill tone="bad">not applied</Pill>
  {:else if s.applied.at}
    <Pill tone="ok">applied</Pill><span class="text-xs">{stamp(s.applied.at)}</span>
  {/if}
</div>
{#if s.applied.errors.length > 0}
  <ul role="alert" class="mt-3 max-w-4xl rounded-md bg-bad-soft px-3 py-2 text-bad">
    {#each s.applied.errors as e (e)}<li class="break-words">{e}</li>{/each}
  </ul>
{/if}
