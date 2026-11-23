<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { describe } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import IdleCard from "#lib/components/power-profile/IdleCard.svelte";
  import ProfileCard from "#lib/components/power-profile/ProfileCard.svelte";
  import { fetchPowerProfile, powerProfileKey, type PowerProfileStatus } from "#lib/power-profile.ts";

  const client = useQueryClient();
  // The idle figures cover the last minute; reading them again each minute keeps up.
  const status = createQuery(() => ({ queryKey: powerProfileKey, queryFn: fetchPowerProfile, refetchInterval: 60_000 }));
  const s = $derived(status.data);
  const saved = (next: PowerProfileStatus) => client.setQueryData(powerProfileKey, next);
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
    <IdleCard current={s} />
  </div>
{:else}
  <p class="mt-4 text-muted">Loading…</p>
{/if}
