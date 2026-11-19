<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { describe } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import BasicsCard from "#lib/components/host/BasicsCard.svelte";
  import DomainCard from "#lib/components/host/DomainCard.svelte";
  import FirewallCard from "#lib/components/host/FirewallCard.svelte";
  import NetworkCard from "#lib/components/host/NetworkCard.svelte";
  import SshCard from "#lib/components/host/SshCard.svelte";
  import TailscaleCard from "#lib/components/host/TailscaleCard.svelte";
  import TunnelCard from "#lib/components/host/TunnelCard.svelte";
  import VpnCard from "#lib/components/host/VpnCard.svelte";
  import { stamp } from "#lib/format.ts";
  import { fetchHostSettings, hostKeys, type HostSettings } from "#lib/host.ts";

  const client = useQueryClient();
  const settings = createQuery(() => ({ queryKey: hostKeys.settings, queryFn: fetchHostSettings }));
  const s = $derived(settings.data);
  const readOnly = $derived(s?.managed === "nixos");
  const saved = (next: HostSettings) => client.setQueryData(hostKeys.settings, next);
</script>

<svelte:head><title>Network · System Settings · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/settings">System Settings</a> /</p>
<h1 class="mt-1 text-xl font-semibold">
  <Help label="Network">The host's own network: its domain, LAN, firewall, VPN and tunnel. A change applies at once and stays across reboots and updates.</Help>
</h1>

{#if settings.error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(settings.error)}</p>
{:else if s}
  <div class="mt-1 flex flex-wrap items-center gap-2 text-muted">
    {#if readOnly}
      <Pill tone="info">nixos</Pill><span>Set in this host's NixOS configuration.</span>
    {:else if s.applied.errors.length > 0}
      <Pill tone="bad">not applied</Pill>
    {:else if s.applied.at}
      <Pill tone="ok">applied</Pill><span class="text-xs">{stamp(s.applied.at)}</span>
    {/if}
  </div>
  {#if s.applied.errors.length > 0}
    <ul role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">
      {#each s.applied.errors as e (e)}<li class="break-words">{e}</li>{/each}
    </ul>
  {/if}
  <!-- Keyed by the stored section: an Apply mounts the card afresh on the new values. -->
  <div class="mt-5 grid max-w-4xl gap-4">
    {#key JSON.stringify(s.domain)}<DomainCard settings={s} {readOnly} onSaved={saved} />{/key}
    {#key JSON.stringify(s.network)}<NetworkCard settings={s} {readOnly} onSaved={saved} />{/key}
    {#key JSON.stringify(s.firewall)}<FirewallCard settings={s} {readOnly} onSaved={saved} />{/key}
    {#key JSON.stringify(s.wireguard)}<VpnCard settings={s} {readOnly} onSaved={saved} />{/key}
    {#key JSON.stringify(s.tunnel)}<TunnelCard settings={s} {readOnly} onSaved={saved} />{/key}
    {#key JSON.stringify(s.tailscale)}<TailscaleCard settings={s} {readOnly} onSaved={saved} />{/key}
    {#key JSON.stringify(s.host)}<BasicsCard settings={s} {readOnly} onSaved={saved} />{/key}
    {#key JSON.stringify(s.host.ssh_keys)}<SshCard settings={s} {readOnly} onSaved={saved} />{/key}
  </div>
{:else}
  <p class="mt-4 text-muted">Loading…</p>
{/if}
