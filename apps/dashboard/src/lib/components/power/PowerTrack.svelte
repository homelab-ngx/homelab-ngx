<script lang="ts">
  import { Check, X } from "@lucide/svelte";
  import type { Milestone } from "#lib/power.ts";

  interface Props {
    marks: Milestone[];
  }
  let { marks }: Props = $props();

  const said = { done: "done", active: "in progress", waiting: "waiting", failed: "failed" } as const;
</script>

<!-- The run's stages as dots on a line. The shape says the state: a tick,
     a ring with a beating halo, a hollow dot, a cross. The line fills up to
     each stage reached. -->
<ol class="flex items-start" aria-label="Progress">
  {#each marks as m, n (m.label)}
    <li class="relative flex min-w-0 flex-1 flex-col items-center gap-1.5" aria-current={m.state === "active" ? "step" : undefined}>
      {#if n > 0}
        <span aria-hidden="true" class="absolute top-2 right-1/2 h-0.5 w-full -translate-y-1/2 overflow-hidden bg-line">
          <span
            class="block h-full transition-[width] duration-700 ease-out motion-reduce:transition-none {m.state === 'failed' ? 'bg-bad' : 'bg-accent'}"
            style:width={m.state === "waiting" ? "0%" : "100%"}
          ></span>
        </span>
      {/if}
      <span aria-hidden="true" class="relative z-10 grid size-4 place-items-center">
        {#if m.state === "done"}
          <span class="grid size-4 place-items-center rounded-full bg-accent text-surface"><Check class="size-3" strokeWidth={3} /></span>
        {:else if m.state === "failed"}
          <span class="grid size-4 place-items-center rounded-full bg-bad text-surface"><X class="size-3" strokeWidth={3} /></span>
        {:else if m.state === "active"}
          <span class="absolute inset-0 rounded-full bg-accent/50 motion-safe:animate-halo"></span>
          <span class="relative grid size-4 place-items-center rounded-full border-2 border-accent bg-surface">
            <span class="size-1.5 rounded-full bg-accent"></span>
          </span>
        {:else}
          <span class="size-4 rounded-full border-2 border-line bg-surface"></span>
        {/if}
      </span>
      <span class="truncate text-xs {m.state === 'waiting' ? 'text-muted' : m.state === 'active' ? 'font-semibold' : ''}">{m.label}</span>
      <span class="sr-only">{said[m.state]}</span>
    </li>
  {/each}
</ol>
