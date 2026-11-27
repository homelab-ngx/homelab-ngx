<script lang="ts">
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import Switch from "#lib/components/Switch.svelte";
  import { stamp } from "#lib/format.ts";
  import { loginMinutesLeft, shortTunnelId, tunnelPill, type HostSettings, type Login, type TunnelStatus } from "#lib/host.ts";

  interface Props {
    tunnel: HostSettings["tunnel"];
    managed: HostSettings["managed"];
    status: TunnelStatus | undefined;
    login: Login | undefined;
    readOnly: boolean;
    pending: boolean;
    error: string;
    onToggle: (on: boolean) => void;
    onConnect: () => void;
    onCancel: () => void;
  }
  let { tunnel, managed, status, login, readOnly, pending, error, onToggle, onConnect, onCancel }: Props = $props();

  const pill = $derived(tunnelPill(status, login, tunnel.enabled, tunnel.credentials_set, managed));
  const waiting = $derived(login?.state === "waiting");
  const minsLeft = $derived(loginMinutesLeft(login?.expires_at));
  const detail = $derived.by(() => {
    if (waiting) return minsLeft > 0 ? `Waiting for approval · ${minsLeft} min left` : "Waiting for approval…";
    if (managed === "image" && !tunnel.credentials_set) return "Not connected";
    if (!tunnel.enabled) return "";
    if (status?.state === "connected" || status?.state === "degraded") {
      const base = `${status.connections} of ${status.want}`;
      return status.colos.length ? `${base} · ${status.colos.join(" · ")}` : base;
    }
    if (status?.state === "down" && status.error) return status.error;
    return "";
  });
  const tunnelLabel = $derived(tunnel.name ? `${tunnel.name} · ${shortTunnelId(tunnel.tunnel_id)}` : shortTunnelId(tunnel.tunnel_id));
  const errors = $derived(status?.recent_errors ?? []);
</script>

<section class="rounded-lg border border-line bg-surface p-4">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h2 class="text-sm font-semibold">Connection</h2>
    {#if tunnel.credentials_set && !readOnly}
      <div class="flex items-center gap-2">
        <Switch checked={tunnel.enabled} label="Cloudflare Tunnel" disabled={pending} onChange={onToggle} />
        <span class="text-xs text-muted">{tunnel.enabled ? "on" : "off"}</span>
      </div>
    {/if}
  </div>

  <div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1">
    <Pill tone={pill.tone}>{pill.word}</Pill>
    {#if detail}<span class="text-muted">{detail}</span>{/if}
  </div>

  {#if !readOnly && waiting}
    <div class="mt-3 flex flex-wrap items-center gap-2">
      {#if login?.url}
        <a class="btn btn-primary" href={login.url} target="_blank" rel="noopener noreferrer">Approve at Cloudflare</a>
      {/if}
      <button type="button" class="btn" disabled={pending} onclick={onCancel}>Cancel</button>
    </div>
  {:else if !readOnly && !tunnel.credentials_set}
    <div class="mt-3">
      <button type="button" class="btn btn-primary" disabled={pending} onclick={onConnect}>
        {pending ? "Starting…" : "Connect to Cloudflare"}
      </button>
    </div>
  {/if}

  {#if tunnel.credentials_set}
    <dl class="mt-3 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1">
      {#if tunnelLabel}
        <dt class="text-muted">
          <Help label="Tunnel">Locally managed tunnel created at Cloudflare during login.</Help>
        </dt>
        <dd class="truncate" title={tunnel.tunnel_id}>{tunnelLabel}</dd>
      {/if}
      {#if tunnel.zone}
        <dt class="text-muted">Zone</dt>
        <dd class="truncate">{tunnel.zone}</dd>
      {/if}
      <dt class="text-muted">
        <Help label="DNS">CNAME records at Cloudflare are added by the host after login, or added by hand when credentials were pasted.</Help>
      </dt>
      <dd>{tunnel.dns_managed ? "Added by the host" : "Added by hand"}</dd>
      {#if status?.version}
        <dt class="text-muted">Version</dt>
        <dd>{status.version}</dd>
      {/if}
      {#if status?.started_at}
        <dt class="text-muted">Up since</dt>
        <dd>{stamp(status.started_at)}</dd>
      {/if}
    </dl>
  {/if}

  {#if error || login?.state === "failed"}
    <p role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">{error || login?.error}</p>
  {/if}

  {#if errors.length > 0}
    <Disclosure label="Recent errors" summary="{errors.length} {errors.length === 1 ? 'line' : 'lines'}" class="mt-3">
      <pre class="max-h-48 overflow-auto rounded-md bg-sunken p-2 font-mono text-xs whitespace-pre-wrap break-words">{errors.join("\n")}</pre>
    </Disclosure>
  {/if}
</section>
