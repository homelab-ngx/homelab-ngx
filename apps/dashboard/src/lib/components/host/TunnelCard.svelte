<script lang="ts">
  import { untrack } from "svelte";
  import { api, call } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import { changed, type HostSettings } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  let { settings, readOnly, onSaved }: Props = $props();
  const id = $props.id();
  const stored = () => ({ enabled: settings.tunnel.enabled, routes: settings.tunnel.routes.map((r) => ({ ...r, path: r.path ?? "" })), credentials: "" });
  let draft = $state(untrack(stored));
  const problem = $derived(
    draft.enabled && !settings.tunnel.credentials_set && !draft.credentials.trim() ? "The tunnel needs its credentials file." : "",
  );

  async function apply() {
    const body = {
      enabled: draft.enabled,
      routes: draft.routes.map((r) => ({ hostname: r.hostname, target: r.target, path: r.path || undefined })),
      credentials: draft.credentials.trim() ? draft.credentials : undefined,
    };
    onSaved(await call(api.PUT("/host-settings/tunnel", { body })));
  }
</script>

<HostCard
  title="Tunnel"
  help="A Cloudflare tunnel publishes some private routes on the internet without opening a port. Each route serves one name under the private domain at a public host name."
  {readOnly}
  dirty={changed(draft, stored())}
  {problem}
  {apply}
  reset={() => (draft = stored())}
>
  <label class="flex items-center gap-2"><input type="checkbox" class="accent-accent" bind:checked={draft.enabled} /> On</label>
  <div class="grid gap-1">
    <label for="{id}-creds" class="text-muted">
      <Help label="Credentials file">The JSON file cloudflared tunnel create writes. Stored encrypted; never shown again.</Help>
    </label>
    <textarea
      id="{id}-creds"
      class="input min-h-16"
      spellcheck="false"
      autocomplete="off"
      placeholder={settings.tunnel.credentials_set ? `Stored for tunnel ${settings.tunnel.tunnel_id}; paste to replace` : '{"AccountTag": …, "TunnelID": …, "TunnelSecret": …}'}
      bind:value={draft.credentials}
    ></textarea>
  </div>
  <h3 class="label">Routes</h3>
  {#each draft.routes as r, i (i)}
    <div class="flex flex-wrap items-center gap-2">
      <input class="input min-w-0 flex-1" aria-label="Public host name" placeholder="photos.example.net" autocomplete="off" bind:value={r.hostname} />
      <input class="input w-40" aria-label="Serves" placeholder="photos" autocomplete="off" bind:value={r.target} />
      <input class="input w-28" aria-label="Path" placeholder="every path" autocomplete="off" bind:value={r.path} />
      {#if !readOnly}<button type="button" class="btn btn-sm" onclick={() => draft.routes.splice(i, 1)}>Remove</button>{/if}
    </div>
  {/each}
  {#if !readOnly}
    <div><button type="button" class="btn btn-sm" onclick={() => draft.routes.push({ hostname: "", target: "", path: "" })}>Add a route</button></div>
  {/if}
</HostCard>
