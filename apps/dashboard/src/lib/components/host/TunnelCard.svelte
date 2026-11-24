<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { untrack } from "svelte";
  import { api, call, describe } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { changed, fetchTunnelLogin, hostKeys, type HostSettings, type Login } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  let { settings, readOnly, onSaved }: Props = $props();
  const client = useQueryClient();
  const t = $derived(settings.tunnel);
  const stored = () => ({ enabled: settings.tunnel.enabled, routes: settings.tunnel.routes.map((r) => ({ ...r, path: r.path ?? "" })) });
  let draft = $state(untrack(stored));
  const problem = $derived(draft.enabled && !t.credentials_set ? "Connect to Cloudflare first." : "");

  // The login runs on the host; its state is polled while it waits, and the
  // settings are read again once it has stored the tunnel.
  const login = createQuery(() => ({
    queryKey: hostKeys.tunnelLogin,
    queryFn: async () => {
      const before = client.getQueryData<Login>(hostKeys.tunnelLogin)?.state;
      const l = await fetchTunnelLogin();
      if (before === "waiting" && l.state === "done") await client.invalidateQueries({ queryKey: hostKeys.settings, exact: true });
      return l;
    },
    refetchInterval: (q: { state: { data?: Login } }) => (q.state.data?.state === "waiting" ? 2000 : false),
    enabled: !readOnly,
  }));
  const l = $derived(login.data);

  let pending = $state(false);
  let error = $state("");
  let confirmForget = $state(false);

  async function connect() {
    pending = true;
    error = "";
    try {
      client.setQueryData(hostKeys.tunnelLogin, await call<Login>(api.POST("/host-settings/tunnel/login")));
    } catch (e) {
      error = describe(e);
    } finally {
      pending = false;
    }
  }

  async function forget() {
    pending = true;
    error = "";
    try {
      onSaved(await call(api.POST("/host-settings/tunnel/forget")));
    } catch (e) {
      error = describe(e);
      pending = false;
      confirmForget = false;
    }
  }

  async function apply() {
    const body = { enabled: draft.enabled, routes: draft.routes.map((r) => ({ hostname: r.hostname, target: r.target, path: r.path || undefined })) };
    onSaved(await call(api.PUT("/host-settings/tunnel", { body })));
  }
</script>

<HostCard
  subtitle="Publishes chosen routes on the internet, no open port."
  {readOnly}
  dirty={changed(draft, stored())}
  {problem}
  {apply}
  reset={() => (draft = stored())}
>
  {#snippet status()}
    {#if l?.state === "waiting"}<Pill tone="info">connecting</Pill>
    {:else if t.enabled && t.credentials_set}<Pill tone="ok">on</Pill>
    {:else}<Pill tone="idle">off</Pill>{/if}
  {/snippet}
  <label class="flex items-center gap-2"><input type="checkbox" class="accent-accent" bind:checked={draft.enabled} /> On</label>
  {#if t.credentials_set}
    <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1">
      <dt class="text-muted">Tunnel</dt><dd class="truncate">{t.tunnel_id}</dd>
      <dt class="text-muted"><Help label="DNS records">Each route's public name points at the tunnel through a DNS record at Cloudflare. A tunnel connected here has them added by the host; one whose credentials were pasted through the API needs them added by hand.</Help></dt>
      <dd>{t.dns_managed ? "Added by the host" : "Added by hand"}</dd>
    </dl>
  {/if}
  {#if !readOnly}
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
      {#if l?.state === "waiting" && l.url}
        <a class="btn btn-primary" href={l.url} target="_blank" rel="noopener noreferrer">Approve at Cloudflare</a>
        <span class="text-muted">Waiting for approval…</span>
      {:else if !t.dns_managed}
        <button type="button" class="btn" disabled={pending} onclick={connect}>{pending ? "Starting…" : "Connect to Cloudflare"}</button>
      {/if}
      {#if t.credentials_set}<button type="button" class="btn btn-sm btn-danger" disabled={pending} onclick={() => (confirmForget = true)}>Forget…</button>{/if}
    </div>
  {/if}
  {#if error || l?.state === "failed"}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error || l?.error}</p>{/if}
  <h4 class="label">Routes</h4>
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

<Modal open={confirmForget} onOpenChange={(o) => (confirmForget = o)} title="Forget the tunnel?">
  <p>The host drops the tunnel's credentials and stops it. The routes stay here.</p>
  <p class="mt-2 text-muted">The tunnel and its DNS records stay at Cloudflare until deleted there.</p>
  {#snippet actions()}
    <button type="button" class="btn btn-danger" disabled={pending} onclick={forget}>Forget</button>
  {/snippet}
</Modal>
