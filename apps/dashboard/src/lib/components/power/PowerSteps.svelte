<script lang="ts">
  import { slide } from "svelte/transition";
  import { prefersReducedMotion } from "svelte/motion";
  import Pill from "#lib/components/Pill.svelte";
  import type { Bar, Row } from "#lib/power.ts";

  interface Props {
    rows: Row[];
  }
  let { rows }: Props = $props();

  const fill: Record<Exclude<Bar, "run">, string> = {
    empty: "w-0 bg-accent",
    full: "w-full bg-ok",
    warn: "w-full bg-warn",
    bad: "w-full bg-bad",
    unknown: "w-full bg-line",
  };
</script>

<ul class="divide-y divide-line">
  {#each rows as r (r.key)}
    <li class="flex min-h-9 items-center gap-3 py-1.5" title={r.detail} in:slide={{ duration: prefersReducedMotion.current ? 0 : 220 }}>
      <div class="min-w-0 flex-1">
        <p class="truncate">{r.name}</p>
        {#if r.wait}<p class="truncate text-xs text-muted tabular-nums">{r.wait.what} · {r.wait.when}</p>{/if}
      </div>
      <span class="relative h-1 w-16 shrink-0 overflow-hidden rounded-full bg-sunken sm:w-24" aria-hidden="true">
        {#if r.bar === "run"}
          <span class="absolute inset-y-0 w-2/5 rounded-full bg-accent motion-safe:animate-run motion-reduce:left-0 motion-reduce:w-full motion-reduce:opacity-50"></span>
        {:else}
          <span class="block h-full rounded-full transition-[width] duration-500 ease-out motion-reduce:transition-none {fill[r.bar]}"></span>
        {/if}
      </span>
      <span class="w-24 shrink-0"><Pill tone={r.tone}>{r.word}</Pill></span>
    </li>
  {/each}
</ul>
