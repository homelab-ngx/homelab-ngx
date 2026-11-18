<script lang="ts">
  import { bytes } from "#lib/format.ts";
  import { percent, type RunDetail } from "#lib/backups.ts";

  interface Props {
    /** The run in flight, as last read; undefined while the first read is out. */
    run: RunDetail | undefined;
    /** One line, for a table row. */
    compact?: boolean;
  }
  let { run, compact = false }: Props = $props();

  const p = $derived(run?.progress ?? null);
  const pct = $derived(p ? percent(p.bytes_done, p.bytes_total) : null);
  const preparing = $derived(!!run && run.items.length === 0);
  const text = $derived.by(() => {
    if (!run) return "Loading…";
    if (preparing) return "Getting ready…";
    if (!p) return "Working…";
    const size = p.bytes_total > 0 ? `${bytes(p.bytes_done)} of ${bytes(p.bytes_total)}` : bytes(p.bytes_done);
    return `${size}${p.speed > 0 ? ` · ${bytes(p.speed)}/s` : ""}`;
  });
</script>

<div class={compact ? "mt-1 min-w-40" : ""} role="status">
  {#if p && !compact}
    <p>
      Copying {p.folder} to {p.destination}
    </p>
  {:else if preparing && !compact}
    <p>Getting ready: stacks prepare their data and the host saves its settings.</p>
  {/if}
  {#if p}
    <div
      class="mt-1 h-1.5 overflow-hidden rounded-full bg-sunken"
      role="progressbar"
      aria-label="Copied so far"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={pct ?? undefined}
    >
      <div class="h-full rounded-full bg-accent transition-[width] {pct === null ? 'w-1/4 motion-safe:animate-pulse' : ''}" style:width={pct === null ? undefined : `${pct}%`}></div>
    </div>
  {/if}
  {#if compact || p}
    <p class="mt-1 text-xs text-muted tabular-nums">
      {#if compact && p}<span class="max-md:hidden">{p.folder} · </span>{/if}{text}{pct !== null ? ` · ${pct}%` : ""}
    </p>
  {/if}
</div>
