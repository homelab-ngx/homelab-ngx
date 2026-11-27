<script lang="ts">
  import { goto } from "$app/navigation";
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Stack } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import RowMenu, { type MenuItem } from "#lib/components/RowMenu.svelte";
  import AppliedStatus from "#lib/components/host/AppliedStatus.svelte";
  import TunnelConnection from "#lib/components/host/TunnelConnection.svelte";
  import TunnelRoutes from "#lib/components/host/TunnelRoutes.svelte";
  import SettingsHead from "#lib/components/settings/SettingsHead.svelte";
  import {
    fetchHostSettings,
    fetchTunnelLogin,
    fetchTunnelStatus,
    hostKeys,
    tunnelPill,
    type HostSettings,
    type Login,
    type TunnelStatus,
  } from "#lib/host.ts";
  import { settingsStatusKey } from "#lib/settings.ts";

  const client = useQueryClient();

  const settings = createQuery(() => ({
    queryKey: hostKeys.settings,
    queryFn: fetchHostSettings,
  }));
  const s = $derived(settings.data);
  const readOnly = $derived(s?.managed === "nixos");

  const statusQuery = createQuery(() => ({
    queryKey: hostKeys.tunnelStatus,
    queryFn: fetchTunnelStatus,
    enabled: Boolean(s),
    refetchInterval: (q: { state: { data?: TunnelStatus } }) => (q.state.data?.state === "starting" ? 2000 : 15_000),
  }));

  const loginQuery = createQuery(() => ({
    queryKey: hostKeys.tunnelLogin,
    queryFn: async () => {
      const before = client.getQueryData<Login>(hostKeys.tunnelLogin)?.state;
      const l = await fetchTunnelLogin();
      if (before === "waiting" && l.state === "done") {
        await client.invalidateQueries({ queryKey: hostKeys.settings, exact: true });
        await client.invalidateQueries({ queryKey: hostKeys.tunnelStatus, exact: true });
        await client.invalidateQueries({ queryKey: settingsStatusKey });
      }
      return l;
    },
    refetchInterval: (q: { state: { data?: Login } }) => (q.state.data?.state === "waiting" ? 2000 : false),
    enabled: Boolean(s) && !readOnly,
  }));

  const stacksQuery = createQuery(() => ({
    queryKey: ["daemon", "stacks"] as const,
    queryFn: () => call<Stack[]>(api.GET("/stacks")),
    enabled: Boolean(s) && !readOnly,
    staleTime: 30_000,
  }));

  let pending = $state(false);
  let error = $state("");
  let confirmForget = $state(false);

  const st = $derived(statusQuery.data);
  const l = $derived(loginQuery.data);
  const headerPill = $derived(s ? tunnelPill(st, l, s.tunnel.enabled, s.tunnel.credentials_set, s.managed) : null);

  function onSaved(next: HostSettings) {
    client.setQueryData(hostKeys.settings, next);
    void client.invalidateQueries({ queryKey: hostKeys.tunnelStatus });
    void client.invalidateQueries({ queryKey: settingsStatusKey });
  }

  async function toggleEnabled(on: boolean) {
    if (!s || pending) return;
    pending = true;
    error = "";
    try {
      const routes = s.tunnel.routes.map((r) => ({
        hostname: r.hostname,
        target: r.target,
        ...(r.path ? { path: r.path } : {}),
        ...(r.public ? { public: true } : {}),
      }));
      onSaved(await call<HostSettings>(api.PUT("/host-settings/tunnel", { body: { enabled: on, routes } })));
    } catch (e) {
      error = describe(e);
    } finally {
      pending = false;
    }
  }

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

  async function cancelLogin() {
    pending = true;
    error = "";
    try {
      client.setQueryData(hostKeys.tunnelLogin, await call<Login>(api.POST("/host-settings/tunnel/login/cancel")));
    } catch (e) {
      error = describe(e);
    } finally {
      pending = false;
    }
  }

  async function restart() {
    pending = true;
    error = "";
    try {
      const next = await call<TunnelStatus>(api.POST("/host-settings/tunnel/restart"));
      client.setQueryData(hostKeys.tunnelStatus, next);
      void client.invalidateQueries({ queryKey: settingsStatusKey });
    } catch (e) {
      error = describe(e);
    } finally {
      pending = false;
    }
  }

  async function recheckRoute(hostname: string) {
    const next = await call<TunnelStatus>(api.POST("/host-settings/tunnel/routes/{hostname}/check", { params: { path: { hostname } } }));
    client.setQueryData(hostKeys.tunnelStatus, next);
    void client.invalidateQueries({ queryKey: settingsStatusKey });
  }

  async function forget() {
    pending = true;
    error = "";
    try {
      onSaved(await call<HostSettings>(api.POST("/host-settings/tunnel/forget")));
      confirmForget = false;
    } catch (e) {
      error = describe(e);
    } finally {
      pending = false;
    }
  }

  const headerItems = $derived.by((): MenuItem[] => {
    if (!s || !s.tunnel.credentials_set) return [];
    const items: MenuItem[] = [];
    if (!readOnly) {
      items.push({
        label: "Restart",
        disabled: pending || !s.tunnel.enabled,
        title: s.tunnel.enabled ? undefined : "Turn the tunnel on first.",
        onSelect: () => void restart(),
      });
    }
    items.push({ label: "Logs", onSelect: () => void goto("/settings/logs?unit=homelab-cloudflared") });
    if (!readOnly) {
      items.push({ label: "Connect again…", disabled: pending, onSelect: () => void connect() });
      items.push({ label: "Forget…", danger: true, disabled: pending, onSelect: () => (confirmForget = true) });
    }
    return items;
  });
</script>

<SettingsHead title="Cloudflare Tunnel">
  {#snippet help()}
    Publishes chosen routes on the internet over an outbound connection to Cloudflare, with no open ports at home.
  {/snippet}
  {#snippet status()}
    {#if headerPill}<Pill tone={headerPill.tone}>{headerPill.word}</Pill>{/if}
  {/snippet}
  {#snippet actions()}
    <RowMenu vertical name="Cloudflare Tunnel" items={headerItems} />
  {/snippet}
</SettingsHead>

{#if settings.error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(settings.error)}</p>
{:else if s}
  <AppliedStatus settings={s} />
  <div class="mt-5 max-w-4xl space-y-6">
    <TunnelConnection
      tunnel={s.tunnel}
      managed={s.managed}
      status={st}
      login={l}
      {readOnly}
      {pending}
      {error}
      onToggle={toggleEnabled}
      onConnect={connect}
      onCancel={cancelLogin}
    />
    <TunnelRoutes
      tunnel={s.tunnel}
      domain={s.domain}
      status={st}
      stacks={stacksQuery.data}
      {readOnly}
      {onSaved}
      onRecheck={recheckRoute}
    />
  </div>
{:else}
  <p class="mt-4 text-muted">Loading…</p>
{/if}

<Modal open={confirmForget} onOpenChange={(o) => (confirmForget = o)} title="Forget the tunnel?">
  <p>Drops the tunnel's credentials and stops it. Routes stay here.</p>
  <p class="mt-2 text-muted">The tunnel and its DNS records stay at Cloudflare until deleted there.</p>
  {#snippet actions()}
    <button type="button" class="btn btn-danger" disabled={pending} onclick={forget}>Forget</button>
  {/snippet}
</Modal>
