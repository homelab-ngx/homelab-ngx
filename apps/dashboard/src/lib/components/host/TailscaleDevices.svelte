<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { describe } from "#lib/api/client.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { ago } from "#lib/format.ts";
  import { fetchTailscalePeers, hostKeys, type TailscaleStatus } from "#lib/host.ts";

  interface Props {
    status?: TailscaleStatus;
  }
  let { status }: Props = $props();
  let open = $state(false);

  const query = createQuery(() => ({
    queryKey: hostKeys.tailscalePeers,
    queryFn: fetchTailscalePeers,
    enabled: open,
  }));

  const peers = $derived(query.data?.peers);
  const summary = $derived.by(() => {
    const total = peers ? peers.length : (status?.total_peers ?? 0);
    const online = peers ? peers.filter((p) => p.online).length : (status?.online_peers ?? 0);
    if (total === 0) return "";
    return `${total} ${total === 1 ? "device" : "devices"} · ${online} online`;
  });
</script>

<section class="card">
  <Disclosure label="Devices" {summary} section remember="tailscale-devices" bind:open>
    {#if query.isLoading}
      <p class="text-muted">Loading devices…</p>
    {:else if query.error}
      <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(query.error)}</p>
    {:else if !peers || peers.length === 0}
      <p class="text-muted">No other devices on this tailnet.</p>
    {:else}
      <div class="overflow-x-auto rounded-lg border border-line bg-surface">
        <table class="w-full text-left">
          <thead>
            <tr class="border-b border-line bg-sunken">
              <th class="px-3 py-2"><span class="label">Name</span></th>
              <th class="px-3 py-2"><span class="label">State</span></th>
              <th class="px-3 py-2 max-sm:hidden"><span class="label">Path</span></th>
              <th class="px-3 py-2"><span class="label">Last seen</span></th>
            </tr>
          </thead>
          <tbody class="divide-y divide-line">
            {#each peers as p (p.dns_name || p.name)}
              <tr>
                <td class="px-3 py-2 font-medium">{p.name}</td>
                <td class="px-3 py-2"><Pill tone={p.online ? "ok" : "idle"}>{p.online ? "online" : "offline"}</Pill></td>
                <td class="px-3 py-2 text-muted max-sm:hidden">
                  {#if !p.online}
                    —
                  {:else if p.direct}
                    direct
                  {:else if p.relay}
                    relay · {p.relay}
                  {:else}
                    —
                  {/if}
                </td>
                <td class="px-3 py-2 text-muted tabular-nums">{p.online ? "now" : ago(p.last_seen)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Disclosure>
</section>
