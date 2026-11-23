<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import Help from "#lib/components/Help.svelte";
  import { api, call, describe } from "#lib/api/client.ts";
  import CompareCard from "#lib/components/power-profile/CompareCard.svelte";
  import IdleCard from "#lib/components/power-profile/IdleCard.svelte";
  import MeterCard from "#lib/components/power-profile/MeterCard.svelte";
  import ProfileCard from "#lib/components/power-profile/ProfileCard.svelte";
  import { fetchPowerMeter, powerMeterKey, type PowerMeter } from "#lib/power-meter.ts";
  import { fetchPowerProfile, powerProfileKey, type PowerProfileStatus } from "#lib/power-profile.ts";

  const client = useQueryClient();
  // The idle figures cover the last minute; reading them again each minute keeps up.
  const status = createQuery(() => ({ queryKey: powerProfileKey, queryFn: fetchPowerProfile, refetchInterval: 60_000 }));
  const s = $derived(status.data);
  const saved = (next: PowerProfileStatus) => {
    client.setQueryData(powerProfileKey, next);
    // A new profile starts new minutes in the comparison.
    void client.invalidateQueries({ queryKey: ["daemon", "metrics", "power-profiles"] });
  };
  // The plug reports every few seconds; its reading is live enough at 5 s.
  const meter = createQuery(() => ({ queryKey: powerMeterKey, queryFn: fetchPowerMeter, refetchInterval: 5_000 }));
  const m = $derived(meter.data);
  const meterSaved = (next: PowerMeter) => client.setQueryData(powerMeterKey, next);
  async function idleOnly(on: boolean) {
    if (!m) return;
    meterSaved(await call<PowerMeter>(api.PUT("/system/power-meter", { body: { url: m.url, entity: m.entity, idle_only: on } })));
  }
</script>

<svelte:head><title>Power profile · System Settings · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/settings">System Settings</a> /</p>
<h1 class="mt-1 text-xl font-semibold">
  <Help label="Power profile">How the host trades speed for power. A change applies at once and again at every start.</Help>
</h1>

{#if status.error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(status.error)}</p>
{:else if s}
  <div class="mt-5 grid max-w-4xl gap-4">
    <!-- Keyed by the stored choice: an Apply mounts the card afresh on the new values. -->
    {#key `${s.profile} ${s.governor}`}<ProfileCard current={s} onSaved={saved} />{/key}
    <IdleCard current={s} wall={m?.watts ?? null} />
    {#if m}
      {#key `${m.url} ${m.entity} ${m.has_token}`}<MeterCard current={m} onSaved={meterSaved} />{/key}
      <CompareCard idleOnly={m.idle_only} metered={m.state !== "off"} onIdleOnly={idleOnly} />
    {/if}
  </div>
{:else}
  <p class="mt-4 text-muted">Loading…</p>
{/if}
