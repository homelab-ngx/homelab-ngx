<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { untrack } from "svelte";
  import { api, call, describe } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { changed, fetchTailscale, hostKeys, joinList, splitList, tailscaleState, type HostSettings, type TailscaleStatus } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  let { settings, readOnly, onSaved }: Props = $props();
  const id = $props.id();
  const client = useQueryClient();
  const stored = () => ({ enabled: settings.tailscale.enabled, routes: joinList(settings.tailscale.advertise_routes) });
  let draft = $state(untrack(stored));

  /** Polled while the host waits for a login, an approval or tailscaled. */
  const waiting = (st: TailscaleStatus | undefined) => st?.state === "starting" || st?.state === "needs-approval" || (st?.state === "needs-login" && !!st.auth_url);
  const status = createQuery(() => ({
    queryKey: hostKeys.tailscale,
    queryFn: fetchTailscale,
    refetchInterval: (q: { state: { data?: TailscaleStatus } }) => (waiting(q.state.data) ? 2000 : false),
  }));
  const st = $derived(status.data);
  const mark = $derived(tailscaleState(st));

  let pending = $state(false);
  let error = $state("");
  let confirmLogout = $state(false);

  async function run(path: "/host-settings/tailscale/login" | "/host-settings/tailscale/logout") {
    pending = true;
    error = "";
    try {
      client.setQueryData(hostKeys.tailscale, await call<TailscaleStatus>(api.POST(path)));
      // A login turns Tailscale on.
      if (!settings.tailscale.enabled) await client.invalidateQueries({ queryKey: hostKeys.settings, exact: true });
    } catch (e) {
      error = describe(e);
    } finally {
      pending = false;
      confirmLogout = false;
    }
  }
</script>

<HostCard
  title="Tailscale"
  subtitle="Reach the host from devices on a tailnet."
  help="Puts the host on a Tailscale network (a tailnet), so its devices reach the host by name with no port opened at home. Log in opens Tailscale's page, where the host is approved from any device. Offered subnets let the tailnet reach the LAN through the host, once approved in the tailnet's admin console."
  {readOnly}
  dirty={changed(draft, stored())}
  apply={async () =>
    onSaved(await call(api.PUT("/host-settings/tailscale", { body: { enabled: draft.enabled, advertise_routes: splitList(draft.routes) } })))}
  reset={() => (draft = stored())}
>
  {#snippet status()}<Pill tone={mark.tone}>{mark.word}</Pill>{/snippet}
  <label class="flex items-center gap-2"><input type="checkbox" class="accent-accent" bind:checked={draft.enabled} /> On</label>
  {#if st?.state === "running"}
    <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1">
      {#if st.tailnet}<dt class="text-muted">Tailnet</dt><dd class="truncate">{st.tailnet}</dd>{/if}
      {#if st.dns_name}<dt class="text-muted">Name</dt><dd class="truncate">{st.dns_name}</dd>{/if}
      <dt class="text-muted">Addresses</dt><dd class="break-words">{st.ips.join(" · ")}</dd>
    </dl>
    {#if !readOnly}<div><button type="button" class="btn btn-sm" disabled={pending} onclick={() => (confirmLogout = true)}>Log out…</button></div>{/if}
  {:else if st?.state === "needs-approval"}
    <p class="text-muted">An admin of the tailnet approves the host in its admin console.</p>
  {:else if !readOnly}
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
      {#if st?.auth_url}
        <a class="btn btn-primary" href={st.auth_url} target="_blank" rel="noopener noreferrer">Approve at Tailscale</a>
        <span class="text-muted">Waiting for approval…</span>
      {:else}
        <button type="button" class="btn" disabled={pending} onclick={() => run("/host-settings/tailscale/login")}>
          {pending ? "Starting…" : "Log in to Tailscale"}
        </button>
      {/if}
    </div>
  {/if}
  {#if error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
  <div class="grid gap-1">
    <label for="{id}-routes" class="text-muted">Offered subnets</label>
    <input id="{id}-routes" class="input" autocomplete="off" placeholder="192.168.1.0/24" bind:value={draft.routes} />
  </div>
</HostCard>

<Modal open={confirmLogout} onOpenChange={(o) => (confirmLogout = o)} title="Log the host out of Tailscale?">
  <p>The tailnet's devices lose their way to the host until it logs in again.</p>
  {#snippet actions()}
    <button type="button" class="btn btn-danger" disabled={pending} onclick={() => run("/host-settings/tailscale/logout")}>Log out</button>
  {/snippet}
</Modal>
