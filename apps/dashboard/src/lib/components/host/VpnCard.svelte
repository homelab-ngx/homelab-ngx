<script lang="ts">
  import { untrack } from "svelte";
  import { api, call } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import { changed, peerConfig, type HostSettings, type Peer } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  let { settings, readOnly, onSaved }: Props = $props();
  const id = $props.id();
  const stored = () => {
    const w = settings.wireguard;
    return { enabled: w.enabled, listen_port: w.listen_port ?? 0, address: w.address ?? "", endpoint: w.endpoint ?? "", peers: w.peers.map((p) => ({ ...p })) };
  };
  let draft = $state(untrack(stored));
  let shown = $state<Peer | null>(null);
  const problem = $derived(draft.enabled && !draft.address.trim() ? "Give the host's VPN address, such as 10.10.0.1/24." : "");
</script>

<HostCard
  title="VPN"
  help="A WireGuard server for the operator's own devices away from home. Each device has its own key; the host gives it an address in the VPN."
  {readOnly}
  dirty={changed(draft, stored())}
  {problem}
  apply={async () => onSaved(await call(api.PUT("/host-settings/wireguard", { body: { ...draft, listen_port: Number(draft.listen_port) || 0 } })))}
  reset={() => (draft = stored())}
>
  <label class="flex items-center gap-2"><input type="checkbox" class="accent-accent" bind:checked={draft.enabled} /> On</label>
  <div class="grid gap-3 sm:grid-cols-3">
    <div class="grid gap-1">
      <label for="{id}-addr" class="text-muted"><Help label="Address">The host's address and the VPN's subnet.</Help></label>
      <input id="{id}-addr" class="input" autocomplete="off" placeholder="10.10.0.1/24" bind:value={draft.address} />
    </div>
    <div class="grid gap-1">
      <label for="{id}-port" class="text-muted">Port</label>
      <input id="{id}-port" class="input" type="number" min="0" max="65535" placeholder="51820" bind:value={draft.listen_port} />
    </div>
    <div class="grid gap-1">
      <label for="{id}-ep" class="text-muted"><Help label="Endpoint">How devices reach the host from outside, for their configs.</Help></label>
      <input id="{id}-ep" class="input" autocomplete="off" placeholder="vpn.example.com:51820" bind:value={draft.endpoint} />
    </div>
  </div>
  {#if settings.wireguard.public_key}
    <p class="text-xs break-all text-muted">Host key {settings.wireguard.public_key}</p>
  {/if}
  <h3 class="label">Devices</h3>
  {#each draft.peers as p, i (i)}
    <div class="flex flex-wrap items-center gap-2">
      <input class="input w-32" aria-label="Name" placeholder="phone" autocomplete="off" bind:value={p.name} />
      <input class="input min-w-0 flex-1" aria-label="Public key" placeholder="public key" autocomplete="off" bind:value={p.public_key} />
      <input class="input w-36" aria-label="Address" placeholder="10.10.0.2" autocomplete="off" bind:value={p.address} />
      {#if settings.wireguard.public_key}<button type="button" class="btn btn-sm" onclick={() => (shown = p)}>Config</button>{/if}
      {#if !readOnly}<button type="button" class="btn btn-sm" onclick={() => draft.peers.splice(i, 1)}>Remove</button>{/if}
    </div>
  {/each}
  {#if !readOnly}
    <div><button type="button" class="btn btn-sm" onclick={() => draft.peers.push({ name: "", public_key: "", address: "" })}>Add a device</button></div>
  {/if}
</HostCard>

{#if shown}
  <Modal open={true} onOpenChange={(o) => !o && (shown = null)} title="Config for {shown.name}" description="Put the device's own private key in.">
    <pre class="overflow-x-auto rounded-md bg-sunken p-3 text-xs">{peerConfig(settings.wireguard, shown)}</pre>
  </Modal>
{/if}
