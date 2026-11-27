<script lang="ts">
  import { ExternalLink } from "@lucide/svelte";
  import { api, call, describe, type Stack } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import RowMenu, { type MenuItem } from "#lib/components/RowMenu.svelte";
  import { routePill, type HostSettings, type Route, type RouteStatus, type TunnelStatus } from "#lib/host.ts";
  import TunnelRouteForm from "./TunnelRouteForm.svelte";

  interface Props {
    tunnel: HostSettings["tunnel"];
    domain: HostSettings["domain"];
    status: TunnelStatus | undefined;
    stacks: Stack[] | undefined;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
    onRecheck: (hostname: string) => Promise<void>;
  }
  let { tunnel, domain, status, stacks, readOnly, onSaved, onRecheck }: Props = $props();

  let formOpen = $state(false);
  let editIndex = $state<number | null>(null);
  let removeIndex = $state<number | null>(null);
  let removing = $state(false);
  let checkingHost = $state("");
  let error = $state("");

  const tunnelOn = $derived(tunnel.enabled && tunnel.credentials_set);
  const statusMap = $derived(
    new Map<string, RouteStatus>((status?.routes ?? []).map((r) => [`${r.hostname.toLowerCase()}\0${r.path ?? ""}`, r])),
  );
  const removeTarget = $derived(removeIndex !== null ? tunnel.routes[removeIndex] : undefined);

  function statusFor(r: Route): RouteStatus | undefined {
    return statusMap.get(`${r.hostname.toLowerCase()}\0${r.path ?? ""}`);
  }

  function openAdd() {
    editIndex = null;
    formOpen = true;
  }

  function openEdit(i: number) {
    editIndex = i;
    formOpen = true;
  }

  async function recheck(hostname: string) {
    checkingHost = hostname;
    error = "";
    try {
      await onRecheck(hostname);
    } catch (e) {
      error = describe(e);
    } finally {
      checkingHost = "";
    }
  }

  async function confirmRemove() {
    if (removeIndex === null || removing) return;
    removing = true;
    error = "";
    const routes = tunnel.routes
      .filter((_, idx) => idx !== removeIndex)
      .map((r) => ({
        hostname: r.hostname,
        target: r.target,
        ...(r.path ? { path: r.path } : {}),
        ...(r.public ? { public: true } : {}),
      }));
    try {
      const saved = await call<HostSettings>(api.PUT("/host-settings/tunnel", { body: { enabled: tunnel.enabled, routes } }));
      onSaved(saved);
      removeIndex = null;
    } catch (e) {
      error = describe(e);
    } finally {
      removing = false;
    }
  }

  function rowItems(r: Route, i: number): MenuItem[] {
    const items: MenuItem[] = [];
    if (!readOnly) items.push({ label: "Edit…", onSelect: () => openEdit(i) });
    items.push({
      label: checkingHost === r.hostname ? "Checking…" : "Recheck",
      disabled: !tunnelOn || checkingHost === r.hostname,
      title: tunnelOn ? undefined : "Turn the tunnel on first.",
      onSelect: () => void recheck(r.hostname),
    });
    if (!readOnly) items.push({ label: "Remove…", danger: true, onSelect: () => (removeIndex = i) });
    return items;
  }
</script>

<section>
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h2 class="label">Routes ({tunnel.routes.length})</h2>
    {#if !readOnly}
      <button type="button" class="btn btn-sm" onclick={openAdd}>Add a route</button>
    {/if}
  </div>

  {#if error}
    <p role="alert" class="mt-2 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
  {/if}

  {#if tunnel.routes.length === 0}
    <p class="mt-2 rounded-lg border border-line bg-surface px-4 py-3 text-muted">No routes yet.</p>
  {:else}
    <div class="mt-2 overflow-x-auto rounded-lg border border-line bg-surface">
      <table class="w-full border-collapse text-left">
        <thead>
          <tr class="border-b border-line bg-sunken">
            <th class="label px-3 py-2">Public name</th>
            <th class="label px-3 py-2 max-sm:hidden">Serves</th>
            <th class="label px-3 py-2">State</th>
            <th class="w-10 px-3 py-2"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each tunnel.routes as r, i (`${r.hostname}\0${r.path ?? ""}`)}
            {@const rs = statusFor(r)}
            {@const pill = routePill(rs, tunnelOn)}
            <tr class="border-t border-line first:border-t-0">
              <td class="px-3 py-2.5">
                <a class="link inline-flex items-center gap-1" href="https://{r.hostname}" target="_blank" rel="noopener noreferrer">
                  {r.hostname}<ExternalLink class="size-3 shrink-0" aria-hidden="true" />
                </a>
                <div class="text-xs text-muted sm:hidden">{r.target}{r.path ? ` · ${r.path}` : ""}</div>
                {#if rs?.dns === "failed" && rs.dns_error}
                  <div class="mt-0.5 text-xs text-bad">{rs.dns_error}</div>
                {/if}
              </td>
              <td class="px-3 py-2.5 max-sm:hidden">
                {r.target}{#if r.path}<span class="text-muted"> · {r.path}</span>{/if}
              </td>
              <td class="px-3 py-2.5">
                <Pill tone={pill.tone}>{pill.word}</Pill>
              </td>
              <td class="px-3 py-2.5 text-right">
                <RowMenu name={r.hostname} items={rowItems(r, i)} />
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

{#if formOpen}
  <TunnelRouteForm
    open={formOpen}
    onOpenChange={(o) => (formOpen = o)}
    {tunnel}
    {domain}
    {stacks}
    {editIndex}
    {onSaved}
  />
{/if}

<Modal open={removeIndex !== null} onOpenChange={(o) => !o && (removeIndex = null)} title="Remove route?">
  {#if removeTarget}
    <p>Stops serving {removeTarget.hostname} through the tunnel.</p>
    <p class="mt-2 text-muted">The DNS CNAME record stays at Cloudflare until removed there.</p>
  {/if}
  {#snippet actions()}
    <button type="button" class="btn btn-danger" disabled={removing} onclick={confirmRemove}>
      {removing ? "Removing…" : "Remove"}
    </button>
  {/snippet}
</Modal>
