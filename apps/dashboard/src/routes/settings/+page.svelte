<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import TileBadge from "#lib/components/TileBadge.svelte";
  import { fetchSettingsStatus, SECTIONS, settingsStatusKey, tileLabel } from "#lib/settings.ts";

  // Without an answer the tiles show no badges; every tile still links to its page.
  const status = createQuery(() => ({ queryKey: settingsStatusKey, queryFn: fetchSettingsStatus, refetchInterval: 30_000 }));
  const tiles = $derived(status.data?.tiles ?? {});

  // A tile spans two rows of its section's grid (icon, name), so icons and
  // names line up across the tiles beside it, even when a name wraps.
  const tileClass =
    "group row-span-2 grid grid-rows-subgrid justify-items-center gap-y-1.5 rounded-lg px-2 pt-3.5 pb-3 text-center hover:bg-sunken focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent";
</script>

<svelte:head><title>System Settings · Homelab-ngx</title></svelte:head>

<h1 class="text-xl font-semibold">System Settings</h1>

<div class="mt-5 grid gap-5">
  {#each SECTIONS as section (section.id)}
    <section class="overflow-hidden rounded-lg border border-line bg-surface" aria-labelledby="{section.id}-h">
      <h2 id="{section.id}-h" class="border-b border-line bg-sunken px-3 py-2"><span class="label">{section.title}</span></h2>
      <div class="grid grid-cols-[repeat(auto-fill,minmax(8rem,1fr))] gap-1 p-3">
        {#each section.tiles as t (t.key)}
          {@const st = tiles[t.key]}
          <a href={t.href} class={tileClass} aria-label={tileLabel(t, st)}>
            <span class="relative">
              <t.icon class="size-7 {st?.state === 'off' ? 'text-muted' : t.core ? 'text-core' : 'text-fg'}" strokeWidth={1.5} aria-hidden="true" />
              <TileBadge status={st} />
            </span>
            <span class="font-medium group-hover:underline {t.core ? 'text-core' : 'text-accent'}">{t.name}</span>
          </a>
        {/each}
      </div>
    </section>
  {/each}
</div>
