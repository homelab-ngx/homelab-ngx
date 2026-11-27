<script lang="ts">
  import { untrack } from "svelte";
  import { api, call } from "#lib/api/client.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import StatusMark from "#lib/components/StatusMark.svelte";
  import Switch from "#lib/components/Switch.svelte";
  import {
    changed,
    loginServerProblem,
    subnetChoices,
    subnetProblem,
    tailscaleExpiryDays,
    type HostSettings,
    type TailscaleLoginInput,
    type TailscaleStatus,
  } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
    status?: TailscaleStatus;
    loginPending?: boolean;
    loginError?: string;
    onLogin?: (input: TailscaleLoginInput) => Promise<boolean>;
  }
  let { settings, readOnly, onSaved, status: st, loginPending = false, loginError = "", onLogin }: Props = $props();

  const id = $props.id();
  let rowSeq = 0;
  const makeRows = (list: readonly string[] | undefined) => (list ?? []).map((cidr) => ({ id: rowSeq++, cidr }));

  const stored = () => ({
    enabled: settings.tailscale.enabled,
    routes: [...(settings.tailscale.advertise_routes ?? [])],
    exit_node: settings.tailscale.exit_node ?? false,
    accept_dns: settings.tailscale.accept_dns ?? true,
    ssh: settings.tailscale.ssh ?? false,
  });

  let draft = $state(untrack(() => ({ ...stored(), rows: makeRows(stored().routes) })));
  let authKey = $state("");
  let loginServer = $state(untrack(() => settings.tailscale.login_server ?? ""));

  const draftRoutes = $derived(draft.rows.map((r) => r.cidr.trim()).filter(Boolean));
  const draftValue = $derived({
    enabled: draft.enabled,
    routes: draftRoutes,
    exit_node: draft.exit_node,
    accept_dns: draft.accept_dns,
    ssh: draft.ssh,
  });
  const dirty = $derived(changed(draftValue, stored()));
  const problem = $derived(subnetProblem(draft.rows.map((r) => r.cidr)));
  const serverProblem = $derived(loginServerProblem(loginServer));
  const choices = $derived(subnetChoices(settings, draftRoutes));

  const routeApproved = $derived.by(() => {
    const m = new Map<string, boolean>();
    for (const r of st?.routes ?? []) m.set(r.prefix, r.approved);
    return m;
  });

  const expiryDays = $derived(tailscaleExpiryDays(st));
  const expiryDate = $derived(
    st?.key_expiry ? new Date(st.key_expiry).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" }) : "off",
  );
  const showIdentity = $derived(
    st !== undefined && (st.state === "running" || st.state === "needs-approval" || st.state === "stopped" || st.state === "expired"),
  );
  const showLoginControls = $derived(
    !readOnly && st?.state !== "running" && st?.state !== "needs-approval",
  );
  const canSubmitOther = $derived(
    !loginPending && !serverProblem && (authKey.trim() !== "" || loginServer.trim() !== (settings.tailscale.login_server ?? "")),
  );
  const advancedSummary = $derived(`tailnet DNS ${draft.accept_dns ? "on" : "off"}, SSH ${draft.ssh ? "on" : "off"}`);

  function routePill(raw: string): { tone: "ok" | "warn" | "idle"; word: string } {
    const c = raw.trim();
    if (!c || !st || st.state === "off" || st.state === "needs-login" || st.state === "starting") {
      return { tone: "idle", word: "not offered yet" };
    }
    if (st.state === "running" && routeApproved.has(c)) {
      return routeApproved.get(c) ? { tone: "ok", word: "approved" } : { tone: "warn", word: "pending" };
    }
    if (routeApproved.has(c)) {
      return { tone: "warn", word: "pending" };
    }
    return { tone: "idle", word: "not offered yet" };
  }

  function addSubnet(cidr = "") {
    draft.rows.push({ id: rowSeq++, cidr });
  }

  async function submitOtherLogin() {
    if (!onLogin || !canSubmitOther) return;
    const key = authKey.trim();
    const srv = loginServer.trim();
    const serverChanged = srv !== (settings.tailscale.login_server ?? "");
    const ok = await onLogin({
      auth_key: key || undefined,
      login_server: srv,
      force_reauth: st?.state === "stopped" || st?.state === "expired" || serverChanged,
    });
    if (ok) authKey = "";
  }
</script>

<HostCard
  subtitle="Reach the host from devices on a tailnet."
  {readOnly}
  {dirty}
  {problem}
  apply={async () =>
    onSaved(
      await call(
        api.PUT("/host-settings/tailscale", {
          body: {
            enabled: draft.enabled,
            advertise_routes: draftRoutes,
            exit_node: draft.exit_node,
            accept_dns: draft.accept_dns,
            ssh: draft.ssh,
            login_server: settings.tailscale.login_server,
          },
        }),
      ),
    )}
  reset={() => {
    const s = stored();
    draft = { ...s, rows: makeRows(s.routes) };
  }}
>
  {#if st?.state === "expired" || (st?.state === "running" && expiryDays !== null && expiryDays <= 0)}
    <div role="alert" class="flex flex-wrap items-center justify-between gap-2 rounded-md bg-bad-soft px-3 py-2 text-bad">
      <span>Node key expired; the host left the tailnet.</span>
      {#if !readOnly && onLogin}
        <button type="button" class="btn btn-primary btn-sm" disabled={loginPending} onclick={() => void onLogin({ force_reauth: true })}>
          Log in again
        </button>
      {/if}
    </div>
  {:else if st?.state === "running" && expiryDays !== null && expiryDays > 0 && expiryDays <= 14}
    <div role="status" class="flex flex-wrap items-center justify-between gap-2 rounded-md bg-warn-soft px-3 py-2 text-warn">
      <span>Key expires in {expiryDays} {expiryDays === 1 ? "day" : "days"}.</span>
      {#if !readOnly && onLogin}
        <button type="button" class="btn btn-sm" disabled={loginPending} onclick={() => void onLogin({ force_reauth: true })}>
          Log in again
        </button>
      {/if}
    </div>
  {:else if st?.state === "stopped"}
    <div role="status" class="rounded-md bg-warn-soft px-3 py-2 text-warn">Tailscale is stopped; log in to reconnect.</div>
  {/if}

  {#if st?.health && st.health.length > 0}
    <ul role="status" class="grid gap-1.5 rounded-md bg-warn-soft px-3 py-2 text-warn">
      {#each st.health as msg, i (i)}
        <li class="flex items-start gap-2">
          <StatusMark tone="warn" class="mt-1" />
          <span class="min-w-0 break-words">{msg}</span>
        </li>
      {/each}
    </ul>
  {/if}

  <div class="flex items-center gap-2">
    <Switch checked={draft.enabled} label="Tailscale" disabled={readOnly} onChange={(v) => (draft.enabled = v)} />
    <span aria-hidden="true">On</span>
  </div>

  {#if showIdentity && st}
    <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1">
      {#if st.tailnet}<dt class="text-muted">Tailnet</dt><dd class="truncate">{st.tailnet}</dd>{/if}
      {#if st.dns_name}<dt class="text-muted">Name</dt><dd class="truncate">{st.dns_name}</dd>{/if}
      {#if st.ips.length > 0}<dt class="text-muted">Addresses</dt><dd class="break-words">{st.ips.join(" · ")}</dd>{/if}
      <dt class="text-muted">Key expiry</dt>
      <dd class="truncate">
        {expiryDate}{#if st.tags && st.tags.length > 0} · tagged: {st.tags.map((t) => t.replace(/^tag:/, "")).join(", ")}{:else if st.user} · {st.user}{/if}
      </dd>
      {#if st.version}<dt class="text-muted">Version</dt><dd>{st.version}</dd>{/if}
      {#if !st.admin_url && st.control_url}<dt class="text-muted">Server</dt><dd class="truncate">{st.control_url}</dd>{/if}
    </dl>
  {/if}

  {#if !readOnly && st?.auth_url}
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
      <a class="btn btn-primary" href={st.auth_url} target="_blank" rel="noopener noreferrer">Open Tailscale login</a>
      <span class="text-muted">Waiting for login…</span>
    </div>
  {/if}

  {#if showLoginControls}
    <div class="grid gap-3">
      {#if !st?.auth_url && st?.state !== "expired" && onLogin}
        <div>
          <button type="button" class="btn btn-primary" disabled={loginPending} onclick={() => void onLogin({})}>
            {loginPending ? "Starting…" : "Log in to Tailscale"}
          </button>
        </div>
      {/if}

      {#if onLogin}
        <Disclosure label="Other ways to log in" summary="auth key, login server" boxed>
          <div class="grid gap-3">
            <div class="grid gap-1">
              <div class="text-muted">
                <Help label="Auth key">One-off key to join without opening a login link. Never stored.</Help>
              </div>
              <input
                id="{id}-authkey"
                type="password"
                class="input"
                aria-label="Auth key"
                autocomplete="off"
                placeholder="tskey-auth-..."
                bind:value={authKey}
              />
            </div>
            <div class="grid gap-1">
              <div class="text-muted">
                <Help label="Login server">HTTPS address of a custom server such as Headscale. Empty uses Tailscale.</Help>
              </div>
              <input
                id="{id}-server"
                type="url"
                class="input"
                aria-label="Login server"
                autocomplete="off"
                placeholder="https://headscale.example.net"
                bind:value={loginServer}
              />
              {#if serverProblem}<p class="text-xs text-muted">{serverProblem}</p>{/if}
            </div>
            <div>
              <button type="button" class="btn" disabled={!canSubmitOther} onclick={() => void submitOtherLogin()}>
                {loginPending ? "Logging in…" : authKey.trim() ? "Log in with key" : "Log in"}
              </button>
            </div>
          </div>
        </Disclosure>
      {/if}
    </div>
  {/if}

  {#if loginError}
    <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{loginError}</p>
  {/if}

  <div class="grid gap-2">
    <div>
      <Help label="Subnets" class="font-medium">IPv4 subnets offered to the tailnet. Each needs approval in the tailnet admin console.</Help>
    </div>
    {#if draft.rows.length > 0}
      <div class="grid gap-2">
        {#each draft.rows as row, i (row.id)}
          {@const rp = routePill(row.cidr)}
          <div class="flex flex-wrap items-center gap-2">
            <input
              class="input min-w-44 flex-1"
              aria-label="Subnet {i + 1}"
              placeholder="192.168.1.0/24"
              autocomplete="off"
              bind:value={row.cidr}
            />
            <Pill tone={rp.tone}>{rp.word}</Pill>
            {#if !readOnly}
              <button type="button" class="btn btn-sm" onclick={() => draft.rows.splice(i, 1)}>Remove</button>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
    {#if !readOnly}
      <div class="flex flex-wrap items-center gap-2">
        {#each choices as c (c.cidr)}
          <button type="button" class="btn btn-sm" onclick={() => addSubnet(c.cidr)}>+ {c.cidr} · {c.label}</button>
        {/each}
        <button type="button" class="btn btn-sm" onclick={() => addSubnet()}>Add a subnet</button>
      </div>
    {/if}
  </div>

  <div class="flex flex-wrap items-center justify-between gap-2">
    <div class="flex flex-wrap items-center gap-2">
      <input id="{id}-exit" type="checkbox" class="accent-accent" aria-label="Offer as exit node" bind:checked={draft.exit_node} />
      <Help label="Offer as exit node">Routes internet traffic for tailnet devices through this host once approved.</Help>
    </div>
    {#if draft.exit_node && st && (st.state === "running" || st.state === "needs-approval")}
      {#if st.exit_node === "approved"}
        <Pill tone="ok">approved</Pill>
      {:else if st.exit_node === "pending"}
        <Pill tone="warn">pending</Pill>
      {/if}
    {/if}
  </div>

  <Disclosure label="Advanced" summary={advancedSummary} boxed>
    <div class="grid gap-2">
      <div class="flex flex-wrap items-center gap-2">
        <input id="{id}-dns" type="checkbox" class="accent-accent" aria-label="Use tailnet DNS" bind:checked={draft.accept_dns} />
        <Help label="Use tailnet DNS">Uses the tailnet's MagicDNS names and DNS settings.</Help>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <input id="{id}-ssh" type="checkbox" class="accent-accent" aria-label="Tailscale SSH" bind:checked={draft.ssh} />
        <Help label="Tailscale SSH">Allows SSH over Tailscale according to the tailnet's access rules.</Help>
      </div>
    </div>
  </Disclosure>
</HostCard>
