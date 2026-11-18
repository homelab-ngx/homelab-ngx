<script lang="ts" module>
  export type Tone = "ok" | "warn" | "bad" | "info" | "idle";
</script>

<script lang="ts">
  import type { Snippet } from "svelte";

  interface Props {
    tone: Tone;
    children: Snippet;
  }
  let { tone, children }: Props = $props();

  const tones: Record<Tone, string> = { ok: "text-ok", warn: "text-warn", bad: "text-bad", info: "text-accent", idle: "text-muted" };
</script>

<!-- Status as Unraid draws it: one coloured mark whose shape says the state
     (circle, triangle, cross, diamond, ring), then one lowercase word in the
     text colour. No background (DESIGN.md, Status). -->
<span class="inline-flex items-center gap-1.5 text-sm font-normal whitespace-nowrap lowercase">
  <svg viewBox="0 0 10 10" class="size-2.5 shrink-0 {tones[tone]}" aria-hidden="true">
    {#if tone === "ok"}
      <circle cx="5" cy="5" r="4.5" fill="currentColor" />
    {:else if tone === "warn"}
      <path d="M5 0.5 9.8 9.5H0.2Z" fill="currentColor" />
    {:else if tone === "bad"}
      <path d="M1.5 1.5 8.5 8.5M8.5 1.5 1.5 8.5" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" />
    {:else if tone === "info"}
      <path d="M5 0.3 9.7 5 5 9.7 0.3 5Z" fill="currentColor" />
    {:else}
      <circle cx="5" cy="5" r="3.6" fill="none" stroke="currentColor" stroke-width="1.6" />
    {/if}
  </svg>
  {@render children()}
</span>
