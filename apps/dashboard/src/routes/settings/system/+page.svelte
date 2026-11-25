<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import PowerDialog from "#lib/components/PowerDialog.svelte";
  import AppliedStatus from "#lib/components/host/AppliedStatus.svelte";
  import BasicsCard from "#lib/components/host/BasicsCard.svelte";
  import CompareCard from "#lib/components/power-profile/CompareCard.svelte";
  import IdleCard from "#lib/components/power-profile/IdleCard.svelte";
  import MeterCard from "#lib/components/power-profile/MeterCard.svelte";
  import ProfileCard from "#lib/components/power-profile/ProfileCard.svelte";
  import SettingsHead from "#lib/components/settings/SettingsHead.svelte";
  import { clockTime } from "#lib/format.ts";
  import { fetchHostSettings, hostKeys, type HostSettings } from "#lib/host.ts";
  import { fetchPower, powerKey, type PowerAction } from "#lib/power.ts";
  import { fetchPowerMeter, powerMeterKey, type PowerMeter } from "#lib/power-meter.ts";
  import { fetchPowerProfile, powerProfileKey, type PowerProfileStatus } from "#lib/power-profile.ts";
  import { settingsStatusKey } from "#lib/settings.ts";

  const client = useQueryClient();

  const host = createQuery(() => ({ queryKey: hostKeys.settings, queryFn: fetchHostSettings }));
  const h = $derived(host.data);
  const readOnly = $derived(h?.managed === "nixos");
  function onHostSaved(next: HostSettings) {
    client.setQueryData(hostKeys.settings, next);
    void client.invalidateQueries({ queryKey: settingsStatusKey });
  }

  // The idle figures cover the last minute; reading them again each minute keeps up.
  const profileStatus = createQuery(() => ({ queryKey: powerProfileKey, queryFn: fetchPowerProfile, refetchInterval: 60_000 }));
  const ps = $derived(profileStatus.data);
  const profileSaved = (next: PowerProfileStatus) => {
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

  const power = createQuery(() => ({ queryKey: powerKey, queryFn: fetchPower, refetchInterval: 30_000 }));
  const scheduled = $derived(power.data?.scheduled);
  let action = $state<PowerAction | null>(null);
  let powerError = $state("");
  const cancel = createMutation(() => ({
    mutationFn: () => call(api.DELETE("/system/power/scheduled")),
    onMutate: () => (powerError = ""),
    onError: (e: unknown) => (powerError = `Cancel: ${describe(e)}`),
    onSettled: () => Promise.all([client.invalidateQueries({ queryKey: powerKey }), client.invalidateQueries({ queryKey: settingsStatusKey })]),
  }));
</script>

<SettingsHead title="System Settings">
  {#snippet help()}Host identity, time zone, power profile, power meter, and reboot or shutdown.{/snippet}
</SettingsHead>

{#if host.error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(host.error)}</p>
{:else if h}
  <AppliedStatus settings={h} />
{/if}

<div class="mt-5 grid max-w-4xl gap-4">
  {#if h}
    <!-- Keyed by the stored section: an Apply mounts the card afresh on the new values. -->
    {#key JSON.stringify([h.host.hostname, h.host.timezone])}<BasicsCard settings={h} {readOnly} onSaved={onHostSaved} />{/key}
  {:else if !host.error}
    <p class="text-muted">Loading…</p>
  {/if}

  {#if profileStatus.error}
    <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(profileStatus.error)}</p>
  {:else if ps}
    <!-- Keyed by the stored choice: an Apply mounts the card afresh on the new values. -->
    {#key `${ps.profile} ${ps.governor}`}<ProfileCard current={ps} onSaved={profileSaved} />{/key}
    <IdleCard current={ps} wall={m?.watts ?? null} />
    {#if m}
      {#key `${m.url} ${m.entity} ${m.has_token}`}<MeterCard current={m} onSaved={meterSaved} />{/key}
      <CompareCard idleOnly={m.idle_only} metered={m.state !== "off"} onIdleOnly={idleOnly} />
    {/if}
  {/if}

  <section id="power" class="card scroll-mt-16" aria-labelledby="power-h">
    <h2 id="power-h" class="font-semibold">
      <Help label="Power">Reboot or shut down the host. Stacks stop and pools unmount before the host goes down.</Help>
    </h2>
    {#if powerError}<p role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">{powerError}</p>{/if}
    {#if scheduled}
      <p role="status" class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md bg-accent-soft px-3 py-2 text-accent">
        <span class="grow">{scheduled.action === "reboot" ? "Reboots" : "Shuts down"} at {clockTime(scheduled.at)}</span>
        <button type="button" class="btn btn-sm" disabled={cancel.isPending} onclick={() => cancel.mutate()}>Cancel</button>
      </p>
    {/if}
    <div class="mt-3 flex flex-wrap gap-2">
      <button type="button" class="btn" onclick={() => (action = "reboot")}>Reboot…</button>
      <button type="button" class="btn" onclick={() => (action = "poweroff")}>Shut down…</button>
    </div>
  </section>
</div>

{#if action}<PowerDialog {action} onClose={() => (action = null)} />{/if}
