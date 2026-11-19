<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { fetchNixStore, keys } from "#lib/api/queries.ts";
  import Help from "#lib/components/Help.svelte";
  import { ago, bytes } from "#lib/format.ts";
  import { fullness, systemDisk } from "#lib/storage.ts";

  const store = createQuery(() => ({ queryKey: keys.nixStore, queryFn: fetchNixStore, refetchInterval: 60_000 }));
  const disk = $derived(store.data ? systemDisk(store.data) : null);
  const toneText = { ok: "text-muted", warn: "text-warn", bad: "text-bad", info: "text-muted", idle: "text-muted" } as const;
</script>

<!-- The disk the host is on, with the Nix store's share of it. A host
     installed before the store moved to /var has the store on a partition
     of its own, and the figures are that partition's. -->
{#if disk && store.data}
  <section class="mt-6" aria-labelledby="system-disk-h">
    <h2 id="system-disk-h" class="mb-2 font-semibold">
      <Help label={disk.own ? "Nix store" : "System disk"}>
        {#if disk.own}
          The partition the stacks are built into.
        {:else}
          The disk the host is on. It holds the host's settings, stack data kept outside a pool, and the Nix store the stacks are built into.
        {/if}
        A stack keeps the version it runs and two before it; older ones are deleted after an update, and when the disk runs low.
      </Help>
    </h2>
    <div class="card">
      <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 tabular-nums">
        <p>
          <span class="text-lg">{bytes(disk.used)}</span>
          <span class="text-muted">used of {bytes(disk.total)}</span>
        </p>
        <p class={toneText[fullness(disk.percent)]}>{bytes(disk.free)} free</p>
      </div>
      <div class="mt-2 flex h-2 overflow-hidden rounded-full bg-sunken" role="img" aria-label="{Math.round(disk.percent)}% used">
        {#each disk.parts as p (p.label)}
          <span class="h-full not-first:border-l not-first:border-surface" style:width="{p.percent}%" style:background={p.color}></span>
        {/each}
      </div>
      <div class="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted">
        {#each disk.parts as p (p.label)}
          <span class="inline-flex items-center gap-1.5">
            <span aria-hidden="true" class="size-2 rounded-sm" style:background={p.color}></span>
            <span>{p.label}</span>
            <span class="tabular-nums">{bytes(p.bytes)}</span>
          </span>
        {/each}
        <span class="sm:ml-auto">
          {#if store.data.collecting}
            cleaning up
          {:else if store.data.collected_at}
            cleaned up {ago(store.data.collected_at)}
          {/if}
        </span>
      </div>
    </div>
  </section>
{/if}
