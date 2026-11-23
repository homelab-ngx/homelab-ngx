<script lang="ts">
  import type { Probe } from "#lib/power.ts";

  interface Props {
    /** The last checks, newest last. */
    probes: Probe[];
    /** How many checks were made in all; numbers the ticks so a new one enters on its own. */
    total: number;
    /** Checks without an answer in a row, up to the last one. */
    misses: number;
  }
  let { probes, total, misses }: Props = $props();

  const first = $derived(total - probes.length);
  const answered = $derived(probes.at(-1) !== "miss");
</script>

<!-- One tick per check, newest on the right: a full bar where the host
     answered, a low hollow one where it did not, a full ok one for the
     first answer from the new boot. -->
<div class="flex items-center gap-3">
  <div class="flex h-4 min-w-0 flex-1 items-end justify-end gap-0.5 overflow-hidden" aria-hidden="true">
    {#each probes as p, n (first + n)}
      <span
        class="w-1 shrink-0 origin-bottom rounded-sm motion-safe:animate-tick {p === 'back'
          ? 'h-4 bg-ok'
          : p === 'answer'
            ? 'h-4 bg-accent/60'
            : 'h-1.5 border border-line'}"
      ></span>
    {/each}
  </div>
  <p class="shrink-0 text-xs text-muted tabular-nums">{answered ? "answered" : `no answer · ${misses} checks`}</p>
</div>
