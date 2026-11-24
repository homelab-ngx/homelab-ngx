<script lang="ts">
  import { untrack } from "svelte";
  import { api, call, describe } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { ago } from "#lib/format.ts";
  import { changed, ddnsName, lanAddress, peerConfig, vpnPort, type AddedDevice, type HostSettings, type Peer } from "#lib/host.ts";
  import DeviceConfig from "./DeviceConfig.svelte";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  let { settings, readOnly, onSaved }: Props = $props();
  const id = $props.id();
  const w = $derived(settings.wireguard);
  const stored = () => {
    const w = settings.wireguard;
    return {
      enabled: w.enabled,
      listen_port: w.listen_port ?? 0,
      address: w.address ?? "",
      endpoint: w.endpoint ?? "",
      peers: w.peers.map((p) => ({ ...p })),
      ddns: { enabled: settings.ddns.enabled, name: settings.ddns.name ?? "" },
    };
  };
  let draft = $state(untrack(stored));
  const dirty = $derived(changed(draft, stored()));
  const problem = $derived(draft.enabled && !draft.address.trim() ? "Give the host's VPN address, such as 10.10.0.1/24." : "");

  const port = $derived(vpnPort({ listen_port: Number(draft.listen_port) || 0 }));
  const lan = $derived(lanAddress(settings));
  const record = $derived(ddnsName(settings, draft.ddns.name));
  const ddnsStatus = $derived(settings.ddns.status);
  const canDDNS = $derived(!!settings.domain.domain && !!settings.domain.dns_provider && settings.domain.dns_credentials_set);
  /** Why a device cannot be added yet, or "". */
  const addBlocked = $derived(
    !w.enabled || !w.public_key ? "Turn the VPN on and apply first." : dirty ? "Apply the changes first." : !w.endpoint && !settings.ddns.enabled ? "Set the endpoint first." : "",
  );

  async function apply() {
    const s = stored();
    let next: HostSettings | undefined;
    const { ddns, ...vpn } = draft;
    const { ddns: storedDDNS, ...storedVPN } = s;
    if (changed(vpn, storedVPN)) next = await call(api.PUT("/host-settings/wireguard", { body: { ...vpn, listen_port: Number(vpn.listen_port) || 0 } }));
    if (changed(ddns, storedDDNS)) next = await call(api.PUT("/host-settings/ddns", { body: ddns }));
    if (next) onSaved(next);
  }

  let lookingUp = $state(false);
  let lookupError = $state("");
  async function usePublicAddress() {
    lookingUp = true;
    lookupError = "";
    try {
      const a = await call(api.GET("/host-settings/wireguard/public-address"));
      draft.endpoint = `${a.ip}:${port}`;
    } catch (e) {
      lookupError = describe(e);
    } finally {
      lookingUp = false;
    }
  }

  // Adding a device: a name, then its config once. The new settings are
  // shown when the dialog closes: they remount this card.
  let adding = $state(false);
  let deviceName = $state("");
  let added = $state<AddedDevice | null>(null);
  let addError = $state("");
  let addPending = $state(false);

  async function addDevice(e: SubmitEvent) {
    e.preventDefault();
    addPending = true;
    addError = "";
    try {
      added = await call<AddedDevice>(api.POST("/host-settings/wireguard/peers", { body: { name: deviceName.trim() } }));
    } catch (err) {
      addError = describe(err);
    } finally {
      addPending = false;
    }
  }
  function closeAdd() {
    adding = false;
    if (added) onSaved(added.settings);
    added = null;
    deviceName = "";
    addError = "";
  }

  let shown = $state<Peer | null>(null);
</script>

<HostCard
  subtitle="A VPN server for devices away from home."
  {readOnly}
  {dirty}
  {problem}
  {apply}
  reset={() => (draft = stored())}
>
  {#snippet status()}<Pill tone={w.enabled ? "ok" : "idle"}>{w.enabled ? "on" : "off"}</Pill>{/snippet}
  <label class="flex items-center gap-2"><input type="checkbox" class="accent-accent" bind:checked={draft.enabled} /> On</label>
  <div class="grid items-start gap-3 sm:grid-cols-3">
    <div class="grid gap-1">
      <label for="{id}-addr" class="text-muted"><Help label="Address">The host's address and the VPN's subnet. Devices get the free addresses after it.</Help></label>
      <input id="{id}-addr" class="input" autocomplete="off" placeholder="10.10.0.1/24" bind:value={draft.address} />
    </div>
    <div class="grid gap-1">
      <label for="{id}-port" class="text-muted">Port</label>
      <input id="{id}-port" class="input" type="number" min="0" max="65535" placeholder="51820" bind:value={draft.listen_port} />
    </div>
    <div class="grid gap-1">
      <label for="{id}-ep" class="text-muted">
        <Help label="Endpoint">How devices reach the host from outside, written into their configs. Empty with dynamic DNS on: its name. The public address is asked of Cloudflare, only on a click.</Help>
      </label>
      <input
        id="{id}-ep"
        class="input"
        autocomplete="off"
        placeholder={draft.ddns.enabled && record ? `${record}:${port}` : `vpn.example.com:${port}`}
        bind:value={draft.endpoint}
      />
      {#if !readOnly}
        <div><button type="button" class="btn btn-sm" disabled={lookingUp} onclick={usePublicAddress}>{lookingUp ? "Asking…" : "Use public address"}</button></div>
      {/if}
      {#if lookupError}<p role="alert" class="text-bad">{lookupError}</p>{/if}
    </div>
  </div>

  <div class="grid gap-1">
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
      <label class="flex items-center gap-2">
        <input type="checkbox" class="accent-accent" disabled={!canDDNS && !draft.ddns.enabled} bind:checked={draft.ddns.enabled} />
        <Help label="Dynamic DNS">
          Keeps a name pointed at the home's public address, which can change, by writing it at the domain's DNS provider every time it does. It checks every 5 minutes with Cloudflare. Needs the domain's provider and credentials (Domain).
        </Help>
      </label>
      {#if settings.domain.dns_provider !== "duckdns"}
        <span class="flex items-center gap-1">
          <input class="input w-28" aria-label="Dynamic DNS name" autocomplete="off" placeholder="vpn" bind:value={draft.ddns.name} />
          {#if settings.domain.domain}<span class="text-muted">.{settings.domain.domain}</span>{/if}
        </span>
      {/if}
    </div>
    {#if settings.ddns.enabled && ddnsStatus}
      {#if ddnsStatus.error}
        <p class="break-words text-bad">{ddnsStatus.error}</p>
      {:else}
        <p class="break-words text-muted">{ddnsStatus.record} → {ddnsStatus.ips.join(", ")}{#if ddnsStatus.at}{" · "}{ago(ddnsStatus.at)}{/if}</p>
      {/if}
    {/if}
  </div>

  <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1">
    <dt class="text-muted">
      <Help label="Router">The host's own firewall lets the VPN's port in. The home router forwards it from the internet to the host; without that, devices away from home do not reach it.</Help>
    </dt>
    <dd>Forward UDP {port} to {lan || "the host"}</dd>
    {#if w.public_key}<dt class="text-muted">Host key</dt><dd class="break-all">{w.public_key}</dd>{/if}
  </dl>

  <h4 class="label">Devices</h4>
  {#each draft.peers as p, i (i)}
    <div class="flex flex-wrap items-center gap-2">
      <!-- On a phone: name and address, then the key on its own line, then the buttons. -->
      <input class="input w-32 max-sm:min-w-0 max-sm:flex-1" aria-label="Name" placeholder="phone" autocomplete="off" bind:value={p.name} />
      <input class="input min-w-0 flex-1 max-sm:order-1 max-sm:basis-full" aria-label="Public key" placeholder="public key" autocomplete="off" bind:value={p.public_key} />
      <input class="input w-36 max-sm:w-32" aria-label="Address" placeholder="10.10.0.2" autocomplete="off" bind:value={p.address} />
      {#if w.public_key}<button type="button" class="btn btn-sm max-sm:order-2" onclick={() => (shown = p)}>Config</button>{/if}
      {#if !readOnly}<button type="button" class="btn btn-sm max-sm:order-2" onclick={() => draft.peers.splice(i, 1)}>Remove</button>{/if}
    </div>
  {/each}
  {#if !readOnly}
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn btn-sm" disabled={!!addBlocked} title={addBlocked} onclick={() => (adding = true)}>Add a device…</button>
      <button type="button" class="btn btn-sm" onclick={() => draft.peers.push({ name: "", public_key: "", address: "" })}>Add by public key</button>
    </div>
    {#if addBlocked && w.enabled}<p class="text-muted">{addBlocked}</p>{/if}
  {/if}
</HostCard>

{#if shown}
  <Modal open={true} onOpenChange={(o) => !o && (shown = null)} title="Config for {shown.name}" description="The device's own private key goes in.">
    <pre class="overflow-x-auto rounded-md bg-sunken p-3 text-xs">{peerConfig(settings.wireguard, shown)}</pre>
  </Modal>
{/if}

<Modal open={adding} onOpenChange={(o) => !o && closeAdd()} title={added ? `Config for ${deviceName.trim()}` : "Add a device"} description={added ? "Shown once: the host keeps no copy of the private key." : undefined}>
  {#if added}
    <DeviceConfig config={added.config} name={`${settings.host.hostname || "homelab"}-${deviceName.trim()}`} />
  {:else}
    <form id="{id}-add" class="grid gap-1" onsubmit={addDevice}>
      <label for="{id}-device" class="text-muted">Name</label>
      <input id="{id}-device" class="input" autocomplete="off" placeholder="phone" maxlength="64" bind:value={deviceName} />
      {#if addError}<p role="alert" class="mt-2 rounded-md bg-bad-soft px-3 py-2 text-bad">{addError}</p>{/if}
    </form>
  {/if}
  {#snippet actions()}
    {#if !added}<button type="submit" form="{id}-add" class="btn btn-primary" disabled={addPending || !deviceName.trim()}>{addPending ? "Adding…" : "Add"}</button>{/if}
  {/snippet}
</Modal>
