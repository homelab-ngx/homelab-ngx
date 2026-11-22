<script lang="ts">
  import type { ServiceInfo, Stack } from "#lib/api/client.ts";
  import { bytes, cpu } from "#lib/format.ts";
  import { health, servicesOf, shortName, stackIcon, stackTitle, stateLabel, stateTone, unitIcon, unitTone, unitWord } from "#lib/stacks.ts";
  import type { StacksView } from "#lib/stacksView.svelte.ts";
  import Pill from "./Pill.svelte";
  import StackIcon from "./StackIcon.svelte";
  import StatusMark from "./StatusMark.svelte";
  import UpdateChip from "./UpdateChip.svelte";

  interface Props {
    stacks: Stack[];
    services: ServiceInfo[];
    view: StacksView;
  }
  let { stacks, services, view }: Props = $props();

  const cards = $derived(
    stacks.map((s) => {
      const units = servicesOf(services, s.name);
      return { stack: s, units, health: health(s, units) };
    }),
  );
</script>

<!-- The Stacks overview (DESIGN.md, App Grid): a card per stack, the whole
     card a link to its page. Minimal shows the services as a strip of
     icons, Detailed as a row each. -->
<ul class="grid gap-4 [grid-template-columns:repeat(auto-fill,minmax(18rem,1fr))]">
  {#each cards as { stack: s, units, health: h } (s.name)}
    {@const usage = s.running && s.usage ? `${cpu(s.usage.cpu_percent)} CPU · ${bytes(s.usage.memory_bytes)}` : ""}
    {@const sub = view === "detailed" ? [s.label ? s.name : "", usage].filter(Boolean).join(" · ") : s.label ? s.name : ""}
    <li class="flex">
      <a
        href="/stacks/{s.name}"
        class="flex w-full flex-col gap-3 rounded-lg border border-line bg-surface p-4 text-fg transition-colors hover:border-muted focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
      >
        <div class="flex items-start gap-3.5">
          <StackIcon src={stackIcon(s)} name={stackTitle(s)} size="md" />
          <div class="flex min-w-0 flex-col gap-1.5 pt-px">
            <span class="leading-tight font-semibold">{stackTitle(s)}</span>
            {#if sub}<span class="-mt-1 leading-tight text-muted tabular-nums">{sub}</span>{/if}
            {#if view === "minimal" && units.length > 1}
              <ul class="flex flex-wrap gap-1.5" aria-label="Services">
                {#each units as u (u.name)}
                  {@const tone = unitTone(u)}
                  <li class="relative inline-flex" title="{shortName(s.name, u.name)}: {unitWord(u)}">
                    <StackIcon src={unitIcon(s, u)} name={shortName(s.name, u.name)} size="sm" dim={tone === "idle"} />
                    {#if tone === "bad" || tone === "info" || tone === "warn"}
                      <span class="absolute -right-1 -bottom-1 inline-flex rounded-full bg-surface p-px"><StatusMark {tone} /></span>
                    {/if}
                    <span class="sr-only">{shortName(s.name, u.name)}: {unitWord(u)}</span>
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        </div>
        {#if view === "detailed" && units.length > 0}
          <ul class="-mx-4 border-b border-line" aria-label="Services">
            {#each units as u (u.name)}
              {@const tone = unitTone(u)}
              <li class="flex items-center gap-2.5 border-t border-line px-4 py-1.5 {tone === 'bad' ? 'bg-bad-soft' : ''}">
                <StackIcon src={unitIcon(s, u)} name={shortName(s.name, u.name)} size="sm" dim={tone === "idle"} />
                <span class="min-w-0 flex-1 truncate">{shortName(s.name, u.name)}</span>
                <Pill {tone}>{unitWord(u)}</Pill>
              </li>
            {/each}
          </ul>
        {/if}
        <div class="mt-auto flex min-h-6 flex-wrap items-center justify-between gap-2">
          {#if s.operation && !s.operation.finished_at}
            <Pill tone="info">{s.operation.kind}…</Pill>
          {:else}
            <Pill tone={stateTone[h.state]}>
              {stateLabel[h.state]}{#if s.running && h.active < h.total}&nbsp;<span class="text-muted">{h.active}/{h.total}</span>{/if}
            </Pill>
          {/if}
          <UpdateChip stack={s} />
        </div>
      </a>
    </li>
  {/each}
</ul>
