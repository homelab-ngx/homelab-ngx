<script lang="ts">
  import { untrack } from "svelte";
  import { api, call } from "#lib/api/client.ts";
  import { changed, joinList, splitList, type HostSettings } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  let { settings, readOnly, onSaved }: Props = $props();
  const id = $props.id();
  const stored = () => ({ enabled: settings.tailscale.enabled, routes: joinList(settings.tailscale.advertise_routes) });
  let draft = $state(untrack(stored));
</script>

<HostCard
  title="Tailscale"
  help="Puts the host on a tailnet. Log it in once with tailscale up on the host; the subnets here are then offered to the tailnet."
  {readOnly}
  dirty={changed(draft, stored())}
  apply={async () =>
    onSaved(await call(api.PUT("/host-settings/tailscale", { body: { enabled: draft.enabled, advertise_routes: splitList(draft.routes) } })))}
  reset={() => (draft = stored())}
>
  <label class="flex items-center gap-2"><input type="checkbox" class="accent-accent" bind:checked={draft.enabled} /> On</label>
  <div class="grid gap-1">
    <label for="{id}-routes" class="text-muted">Offered subnets</label>
    <input id="{id}-routes" class="input" autocomplete="off" placeholder="192.168.1.0/24" bind:value={draft.routes} />
  </div>
</HostCard>
