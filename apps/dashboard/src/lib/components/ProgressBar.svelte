<script lang="ts">
  import { prefersReducedMotion, Tween } from "svelte/motion";

  interface Props {
    /** How far, 0 to 1. */
    value: number;
    /** What the bar measures, for a screen reader. */
    label: string;
    tone?: "accent" | "ok" | "bad";
    /** Work is under way: a light sheen crosses the fill. */
    moving?: boolean;
    /** Past what was expected: the fill pulses. */
    late?: boolean;
    /** The power screen's 8px bar; every other bar is 6px. */
    tall?: boolean;
  }
  let { value, label, tone = "accent", moving = false, late = false, tall = false }: Props = $props();

  // The fill glides to each new value over a second, so a value read once a
  // second moves it all the time.
  const fill = new Tween(0);
  $effect(() => {
    void fill.set(value, { duration: prefersReducedMotion.current ? 0 : 1000 });
  });

  const color = { accent: "bg-accent", ok: "bg-ok", bad: "bg-bad" } as const;
</script>

<div
  class="relative overflow-hidden rounded-full bg-sunken {tall ? 'h-2' : 'h-1.5'}"
  role="progressbar"
  aria-label={label}
  aria-valuemin={0}
  aria-valuemax={100}
  aria-valuenow={Math.round(value * 100)}
>
  <div
    class="relative h-full overflow-hidden rounded-full transition-colors {color[tone]} {late ? 'motion-safe:animate-pulse' : ''}"
    style:width="{Math.max(fill.current, 0.02) * 100}%"
  >
    {#if moving}
      <span class="absolute inset-y-0 left-0 w-1/3 bg-linear-to-r from-transparent via-surface/45 to-transparent motion-safe:animate-sheen motion-reduce:hidden"></span>
    {/if}
  </div>
</div>
