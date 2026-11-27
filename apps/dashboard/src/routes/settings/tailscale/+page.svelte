<script lang="ts">
  import { ChevronDown } from "@lucide/svelte";
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { DropdownMenu } from "bits-ui";
  import { api, call, describe } from "#lib/api/client.ts";
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
  import TailscaleCard from "#lib/components/host/TailscaleCard.svelte";
  import TailscaleDevices from "#lib/components/host/TailscaleDevices.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import StatusMark from "#lib/components/StatusMark.svelte";
  import {
    fetchTailscale,
    hostKeys,
    tailscaleNeedsPoll,
    tailscalePending,
    tailscalePendingMessage,
    tailscaleState,
    type TailscaleLoginInput,
    type TailscaleStatus,
  } from "#lib/host.ts";
  import { settingsStatusKey } from "#lib/settings.ts";

  const client = useQueryClient();

  const status = createQuery(() => ({
    queryKey: hostKeys.tailscale,
    queryFn: fetchTailscale,
    refetchInterval: (q: { state: { data?: TailscaleStatus } }) => tailscaleNeedsPoll(q.state.data),
  }));
  const st = $derived(status.data);
  const mark = $derived(tailscaleState(st));
  const pending = $derived(tailscalePending(st));
  const pendingMsg = $derived(tailscalePendingMessage(st));
  const loggedIn = $derived(
    st !== undefined && (st.state === "running" || st.state === "needs-approval" || st.state === "stopped" || st.state === "expired"),
  );

  let loginPending = $state(false);
  let loginError = $state("");
  let confirmLogout = $state(false);

  async function runLogin(input: TailscaleLoginInput): Promise<boolean> {
    loginPending = true;
    loginError = "";
    try {
      const next = await call<TailscaleStatus>(api.POST("/host-settings/tailscale/login", { body: input }));
      client.setQueryData(hostKeys.tailscale, next);
      await Promise.all([
        client.invalidateQueries({ queryKey: hostKeys.settings, exact: true }),
        client.invalidateQueries({ queryKey: settingsStatusKey }),
      ]);
      return true;
    } catch (e) {
      loginError = describe(e);
      return false;
    } finally {
      loginPending = false;
    }
  }

  async function runLogout() {
    loginPending = true;
    loginError = "";
    try {
      const next = await call<TailscaleStatus>(api.POST("/host-settings/tailscale/logout"));
      client.setQueryData(hostKeys.tailscale, next);
      await client.invalidateQueries({ queryKey: settingsStatusKey });
    } catch (e) {
      loginError = describe(e);
    } finally {
      loginPending = false;
      confirmLogout = false;
    }
  }
</script>

<HostSettingsPage
  title="Tailscale"
  help="Connects the host to a Tailscale network (tailnet) with no open router ports. Approved subnets reach the LAN through the host."
  section={(s) => s.tailscale}
>
  {#snippet actions({ readOnly })}
    <Pill tone={mark.tone}>{mark.word}</Pill>
    {#if loggedIn && (!readOnly || st?.admin_url)}
      <DropdownMenu.Root>
        <DropdownMenu.Trigger class="btn">
          More
          <ChevronDown class="size-3.5 text-muted" aria-hidden="true" />
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal>
          <DropdownMenu.Content align="end" sideOffset={6} class="z-50 min-w-44 rounded-md border border-line bg-surface p-1 shadow-lg">
            {#if !readOnly}
              <DropdownMenu.Item
                disabled={loginPending}
                onSelect={() => void runLogin({ force_reauth: true })}
                class="cursor-default rounded-sm px-2.5 py-1.5 outline-none data-disabled:opacity-50 data-highlighted:bg-sunken"
              >
                Log in again
              </DropdownMenu.Item>
            {/if}
            {#if st?.admin_url}
              <DropdownMenu.Item>
                {#snippet child({ props })}
                  <a
                    {...props}
                    href={st.admin_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    class="block rounded-sm px-2.5 py-1.5 text-fg outline-none hover:bg-sunken data-highlighted:bg-sunken"
                  >
                    Open admin console
                  </a>
                {/snippet}
              </DropdownMenu.Item>
            {/if}
            {#if !readOnly}
              <DropdownMenu.Separator class="my-1 border-t border-line" />
              <DropdownMenu.Item
                disabled={loginPending}
                onSelect={() => (confirmLogout = true)}
                class="cursor-default rounded-sm px-2.5 py-1.5 text-bad outline-none data-disabled:opacity-50 data-highlighted:bg-bad-soft"
              >
                Log out…
              </DropdownMenu.Item>
            {/if}
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>
    {/if}
  {/snippet}

  {#snippet above()}
    {#if pending.any && st}
      <div class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-line bg-surface px-3 py-2.5">
        <span class="flex items-center gap-2">
          <StatusMark tone="warn" />
          <span>{pendingMsg}</span>
        </span>
        {#if st.admin_url}
          <a class="btn btn-sm" href={st.admin_url} target="_blank" rel="noopener noreferrer">Open admin console</a>
        {/if}
      </div>
    {/if}
  {/snippet}

  {#snippet card(p)}
    <TailscaleCard
      {...p}
      onSaved={(next) => {
        p.onSaved(next);
        void client.invalidateQueries({ queryKey: hostKeys.tailscale });
      }}
      status={st}
      {loginPending}
      {loginError}
      onLogin={runLogin}
    />
  {/snippet}

  {#snippet below()}
    {#if st?.state === "running" || (st?.total_peers ?? 0) > 0}
      <TailscaleDevices status={st} />
    {/if}
  {/snippet}
</HostSettingsPage>

<Modal open={confirmLogout} onOpenChange={(o) => (confirmLogout = o)} title="Log the host out of Tailscale?">
  <p>The tailnet's devices lose their way to the host until it logs in again.</p>
  {#snippet actions()}
    <button type="button" class="btn btn-danger" disabled={loginPending} onclick={() => void runLogout()}>Log out</button>
  {/snippet}
</Modal>
