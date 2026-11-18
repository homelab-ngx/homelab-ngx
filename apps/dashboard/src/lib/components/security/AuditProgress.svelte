<script lang="ts">
  import type { SecurityAudit } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { duration } from "#lib/format.ts";
  import { countsLabel } from "#lib/security.ts";

  interface Props {
    audit: SecurityAudit;
  }
  let { audit }: Props = $props();

  const p = $derived(audit.progress);
  const current = $derived(audit.chunks.find((c) => c.area === p.current) ?? audit.chunks.find((c) => c.state === "pending"));

  // The elapsed time moves while the audit runs.
  let now = $state(Date.now());
  $effect(() => {
    const tick = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(tick);
  });
</script>

<section class="card" aria-live="polite" aria-label="Audit progress">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h2 class="font-semibold">
      <Help label="Auditing">
        One area at a time, then a summary. It keeps running when this page is closed, and goes on after the daemon restarts.
      </Help>
    </h2>
    <Pill tone="info">running</Pill>
  </div>
  <div
    class="mt-3 h-1.5 overflow-hidden rounded-full bg-sunken"
    role="progressbar"
    aria-label="Areas done"
    aria-valuemin={0}
    aria-valuemax={100}
    aria-valuenow={p.percent}
  >
    <div class="h-full rounded-full bg-accent transition-[width]" style:width="{Math.max(p.percent, 4)}%"></div>
  </div>
  <p class="mt-2 text-xs text-muted tabular-nums">
    {current?.title ?? "Summary"} · {p.done} of {p.total} · {countsLabel(audit.counts)} · {duration(audit.started_at, null, now)}
  </p>
</section>
