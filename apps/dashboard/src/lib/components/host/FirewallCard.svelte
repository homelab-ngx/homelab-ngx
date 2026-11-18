<script lang="ts">
  import { untrack } from "svelte";
  import { api, call } from "#lib/api/client.ts";
  import { changed, joinList, splitList, type HostSettings, type Opening } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  let { settings, readOnly, onSaved }: Props = $props();
  type Row = { port: number; proto: "tcp" | "udp"; from: string; note: string };
  const stored = (): Row[] => settings.firewall.openings.map((o) => ({ port: o.port, proto: o.proto, from: joinList(o.from), note: o.note ?? "" }));
  let rows = $state(untrack(stored));
  const body = (): Opening[] => rows.map((r) => ({ port: Number(r.port), proto: r.proto, from: splitList(r.from), note: r.note || undefined }));
  const problem = $derived(rows.some((r) => !(Number(r.port) >= 1 && Number(r.port) <= 65535)) ? "A port is 1 to 65535." : "");
</script>

<HostCard
  title="Firewall"
  help="Ports let in besides SSH, HTTP, HTTPS and the VPN, which are always open. Everything else from outside is dropped. A stack's own ports are its grants."
  {readOnly}
  dirty={changed(rows, stored())}
  {problem}
  apply={async () => onSaved(await call(api.PUT("/host-settings/firewall", { body: { openings: body() } })))}
  reset={() => (rows = stored())}
>
  {#each rows as r, i (i)}
    <div class="flex flex-wrap items-center gap-2">
      <input class="input w-24" type="number" min="1" max="65535" aria-label="Port" bind:value={r.port} />
      <select class="input w-20" aria-label="Protocol" bind:value={r.proto}><option>tcp</option><option>udp</option></select>
      <input class="input min-w-0 flex-1" aria-label="From" placeholder="anyone" autocomplete="off" bind:value={r.from} />
      <input class="input min-w-0 flex-1" aria-label="Note" placeholder="note" autocomplete="off" bind:value={r.note} />
      {#if !readOnly}<button type="button" class="btn btn-sm" onclick={() => rows.splice(i, 1)}>Remove</button>{/if}
    </div>
  {/each}
  {#if !readOnly}
    <div><button type="button" class="btn btn-sm" onclick={() => rows.push({ port: 0, proto: "tcp", from: "", note: "" })}>Add an opening</button></div>
  {/if}
</HostCard>
