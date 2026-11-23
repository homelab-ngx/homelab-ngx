<script lang="ts">
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import ProgressBar from "#lib/components/ProgressBar.svelte";
  import { blockerRow, share, watts, type PowerProfileStatus } from "#lib/power-profile.ts";

  interface Props {
    current: PowerProfileStatus;
  }
  let { current }: Props = $props();
  const id = $props.id();
  const idle = $derived(current.idle);
  // "Active" first: the share of time no CPU idled, then each idle state.
  const cpu = $derived([{ name: "Active", latency_us: 0, share: idle.active }, ...idle.cpu_states]);
</script>

{#snippet bars(rows: { name: string; latency_us: number; share: number }[], what: string)}
  <ul class="grid gap-1.5">
    {#each rows as r (r.name)}
      <li class="grid grid-cols-[6rem_minmax(0,1fr)_3rem] items-center gap-3">
        <span>{r.name}{#if r.latency_us}<span class="ml-1.5 text-xs text-muted">{r.latency_us} µs</span>{/if}</span>
        <ProgressBar value={r.share} label="{what} {r.name}" />
        <span class="text-right tabular-nums">{share(r.share)}</span>
      </li>
    {/each}
  </ul>
{/snippet}

<section id="idle" class="card scroll-mt-16" aria-labelledby="{id}-h">
  <div class="flex flex-wrap items-start justify-between gap-x-3 gap-y-1">
    <div class="min-w-0">
      <h3 id="{id}-h" class="font-semibold">
        <Help label="Idle">
          Where the CPU spent the last minute. A deeper state saves more and takes longer to wake. The package reaches a deep state only when every core
          and device sleeps.
        </Help>
      </h3>
      <p class="mt-0.5 text-xs text-muted">The last {Math.round(idle.window_seconds)} s</p>
    </div>
    {#if idle.watts !== null}<p class="pt-0.5 text-lg tabular-nums">{watts(idle.watts)}</p>{/if}
  </div>

  <div class="mt-3 grid gap-3">
    <h4 class="label">CPU</h4>
    {@render bars(cpu, "CPU")}
    {#if idle.package_states.length}
      <h4 class="label">Package{idle.deepest ? ` · deepest ${idle.deepest}` : ""}</h4>
      {@render bars(idle.package_states.map((p) => ({ ...p, latency_us: 0 })), "Package")}
    {/if}
    <h4 class="label">
      <Help label="Awake">Devices that keep the package out of deeper states, and the profile that lets them sleep.</Help>
    </h4>
    {#if current.blockers.length}
      <ul class="grid gap-1.5">
        {#each current.blockers as b (b.kind + b.device)}
          {@const row = blockerRow(b)}
          <li class="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3">
            <span class="min-w-0">{row.name}<span class="block truncate text-xs text-muted" title={row.detail}>{row.detail}</span></span>
            <Pill tone="warn">awake</Pill>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="text-muted">Every device can sleep.</p>
    {/if}
  </div>
</section>
