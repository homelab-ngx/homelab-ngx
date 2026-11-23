<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { describe } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import AppliedStatus from "#lib/components/host/AppliedStatus.svelte";
  import BasicsCard from "#lib/components/host/BasicsCard.svelte";
  import ClockCard from "#lib/components/host/ClockCard.svelte";
  import ConsoleCard from "#lib/components/host/ConsoleCard.svelte";
  import { fetchHostSettings, hostKeys, type HostSettings } from "#lib/host.ts";

  const client = useQueryClient();
  const settings = createQuery(() => ({ queryKey: hostKeys.settings, queryFn: fetchHostSettings }));
  const s = $derived(settings.data);
  const readOnly = $derived(s?.managed === "nixos");
  const saved = (next: HostSettings) => client.setQueryData(hostKeys.settings, next);
</script>

<svelte:head><title>General · System Settings · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/settings">System Settings</a> /</p>
<h1 class="mt-1 text-xl font-semibold">
  <Help label="General">The host's name, its clock and its console. A change applies at once and stays across reboots and updates.</Help>
</h1>

{#if settings.error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(settings.error)}</p>
{:else if s}
  <AppliedStatus settings={s} />
  <!-- Keyed by the stored section: an Apply mounts the card afresh on the new values. -->
  <div class="mt-5 grid max-w-4xl gap-4">
    {#key `${s.host.hostname} ${s.host.timezone}`}<BasicsCard settings={s} {readOnly} onSaved={saved} />{/key}
    {#key JSON.stringify(s.time)}<ClockCard settings={s} {readOnly} onSaved={saved} />{/key}
    {#key JSON.stringify(s.console)}<ConsoleCard settings={s} {readOnly} onSaved={saved} />{/key}
  </div>
{:else}
  <p class="mt-4 text-muted">Loading…</p>
{/if}
