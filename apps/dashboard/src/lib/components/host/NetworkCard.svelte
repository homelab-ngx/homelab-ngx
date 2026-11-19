<script lang="ts">
  import { untrack } from "svelte";
  import { api, call, type Schema } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import { changed, joinList, splitList, type HostSettings } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";
  import Ipv4Fields from "./Ipv4Fields.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  let { settings, readOnly, onSaved }: Props = $props();
  const id = $props.id();
  const storedIPv4 = (): Schema<"IPv4"> => ({ dns: [], ...settings.network.ipv4, mode: settings.network.ipv4?.mode ?? "dhcp" });
  let lan = $state(untrack(() => joinList(settings.network.lan)));
  let ipv4 = $state(untrack(storedIPv4));
</script>

<HostCard
  title="LAN"
  help="The host's IPv4 address on the wired network, and the home network's subnets: a stack's port grant from the LAN lets those clients in. A wrong static address leaves the host unreachable until it is fixed at its console."
  {readOnly}
  dirty={changed(splitList(lan), settings.network.lan) || changed(ipv4, storedIPv4())}
  apply={async () => onSaved(await call(api.PUT("/host-settings/network", { body: { lan: splitList(lan), ipv4 } })))}
  reset={() => {
    lan = joinList(settings.network.lan);
    ipv4 = storedIPv4();
  }}
>
  <div class="grid gap-4">
    <Ipv4Fields bind:value={ipv4} disabled={readOnly} />
    <div class="grid gap-1">
      <label for="{id}-lan" class="text-muted">LAN subnets</label>
      <input id="{id}-lan" class="input" autocomplete="off" placeholder="192.168.1.0/24" bind:value={lan} />
    </div>
    {#if settings.wired.length > 0}
      <dl class="flex flex-wrap gap-x-6 gap-y-1">
        <dt class="text-muted">
          <Help label="MAC address">A powered-off host starts on a Wake-on-LAN packet sent to this address, where its firmware allows it.</Help>
        </dt>
        {#each settings.wired as nic (nic.name)}
          <dd>{nic.mac} <span class="text-muted">{nic.name}</span></dd>
        {/each}
      </dl>
    {/if}
  </div>
</HostCard>
