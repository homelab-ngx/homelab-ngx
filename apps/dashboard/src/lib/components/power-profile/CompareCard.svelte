<script lang="ts">
  import { createQuery, keepPreviousData } from "@tanstack/svelte-query";
  import { describe } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Switch from "#lib/components/Switch.svelte";
  import { compareRows, fetchPowerProfiles, powerProfilesKey, type CompareRange } from "#lib/power-meter.ts";

  interface Props {
    /** Show only idle minutes; stored with the meter's settings. */
    idleOnly: boolean;
    /** Whether a meter is set up, so the wall column means something. */
    metered: boolean;
    onIdleOnly: (on: boolean) => Promise<unknown>;
  }
  let { idleOnly, metered, onIdleOnly }: Props = $props();
  const id = $props.id();
  let range = $state<CompareRange>("7d");
  let saving = $state(false);
  let error = $state("");

  const profiles = createQuery(() => ({
    queryKey: powerProfilesKey(range, idleOnly),
    queryFn: () => fetchPowerProfiles(range, idleOnly),
    refetchInterval: 60_000,
    placeholderData: keepPreviousData,
  }));
  const rows = $derived(compareRows(profiles.data?.profiles ?? []));

  async function toggle(on: boolean) {
    saving = true;
    error = "";
    try {
      await onIdleOnly(on);
    } catch (err) {
      error = describe(err);
    } finally {
      saving = false;
    }
  }
</script>

<section id="compare" class="card scroll-mt-16" aria-labelledby="{id}-h">
  <div class="flex flex-wrap items-start justify-between gap-x-3 gap-y-1">
    <div class="min-w-0">
      <h3 id="{id}-h" class="font-semibold">
        <Help label="Compare">
          Average power under each profile, from the stored minutes. Idle only counts minutes with CPU usage under 5%, so a busy hour does not decide it.
        </Help>
      </h3>
      <p class="mt-0.5 text-xs text-muted">Average draw per profile</p>
    </div>
    <div class="flex flex-wrap items-center gap-3">
      <span class="flex items-center gap-2">
        <Switch checked={idleOnly} label="Idle only" disabled={saving} onChange={toggle} />
        <span aria-hidden="true">Idle only</span>
      </span>
      <select class="input w-auto" aria-label="Range" bind:value={range}>
        <option value="7d">7 days</option>
        <option value="30d">30 days</option>
      </select>
    </div>
  </div>
  {#if error}<p role="alert" class="mt-3 text-bad">{error}</p>{/if}
  {#if profiles.error}
    <p role="alert" class="mt-3 text-bad">{describe(profiles.error)}</p>
  {:else if rows.length}
    <table class="mt-3 w-full text-left {profiles.isPlaceholderData ? 'opacity-60' : ''}">
      <thead>
        <tr>
          <th class="label py-1 pr-3">Profile</th>
          <th class="label py-1 pr-3 text-right">Time</th>
          {#if metered}<th class="label py-1 pr-3 text-right">Wall</th>{/if}
          <th class="label py-1 pr-3 text-right">CPU</th>
          <th class="label py-1 text-right">vs Balanced</th>
        </tr>
      </thead>
      <tbody>
        {#each rows as r (r.name)}
          <tr class="border-t border-line">
            <td class="py-1.5 pr-3">{r.name}</td>
            <td class="py-1.5 pr-3 text-right tabular-nums">{r.time}</td>
            {#if metered}<td class="py-1.5 pr-3 text-right tabular-nums">{r.wall}</td>{/if}
            <td class="py-1.5 pr-3 text-right tabular-nums">{r.cpu}</td>
            <td class="py-1.5 text-right tabular-nums">{r.delta}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else if !profiles.isPending}
    <p class="mt-3 text-muted">Nothing stored yet. Each minute from now on counts.</p>
  {/if}
</section>
