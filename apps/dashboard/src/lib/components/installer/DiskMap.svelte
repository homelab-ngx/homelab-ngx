<script lang="ts">
  import { bytes } from "#lib/format.ts";
  import { HOLDS_WORD, type Holds, type Segment } from "#lib/installer.ts";

  interface Props {
    segments: Segment[];
    /** The disk's size: a segment's width is its share of it. */
    size: number;
    label: string;
    selected?: string | null;
    /** Without it the map only shows. */
    onSelect?: (s: Segment) => void;
  }
  let { segments, size, label, selected = null, onSelect }: Props = $props();

  const tone: Record<Holds, string> = {
    esp: "bg-accent-soft text-accent border-accent/40",
    windows: "bg-warn-soft text-warn border-warn/40",
    linux: "bg-core-soft text-core border-core/40",
    homelab: "bg-ok-soft text-ok border-ok/50",
    empty: "bg-sunken text-muted border-line",
    other: "bg-sunken text-muted border-line",
    free: "free border-dashed border-line text-muted",
  };
</script>

<!-- The disk to scale: every segment as wide as its share, never too narrow to click. -->
<div class="flex h-18 w-full gap-1" role="group" aria-label={label}>
  {#each segments as s (s.key)}
    {@const width = Math.max(s.size, size * 0.015)}
    <svelte:element
      this={onSelect ? "button" : "div"}
      type={onSelect ? "button" : undefined}
      class="flex min-w-10 flex-col justify-center overflow-hidden rounded-md border px-2 text-left {tone[s.holds]} {s.change === 'new' ? 'border-2' : ''} {selected === s.key
        ? 'ring-2 ring-accent ring-offset-2 ring-offset-surface'
        : ''}"
      style="flex: {width} 1 0"
      aria-pressed={onSelect ? selected === s.key : undefined}
      aria-label="{s.names || HOLDS_WORD[s.holds]}, {s.title}, {bytes(s.size)}"
      onclick={onSelect ? () => onSelect(s) : undefined}
      role={onSelect ? undefined : "img"}
    >
      <span class="truncate font-medium">{s.title}</span>
      <span class="truncate text-xs opacity-80">{s.names ? `${s.names} · ` : ""}{bytes(s.size)}</span>
    </svelte:element>
  {/each}
</div>

<style>
  .free {
    background: repeating-linear-gradient(135deg, var(--surface) 0 7px, var(--sunken) 7px 14px);
  }
</style>
