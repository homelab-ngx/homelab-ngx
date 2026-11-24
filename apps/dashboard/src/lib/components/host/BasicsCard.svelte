<script lang="ts">
  import { untrack } from "svelte";
  import { api, call } from "#lib/api/client.ts";
  import { changed, type HostSettings } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  let { settings, readOnly, onSaved }: Props = $props();
  const id = $props.id();
  const stored = () => ({ ...settings.host });
  let draft = $state(untrack(stored));
  const zones = Intl.supportedValuesOf("timeZone");
</script>

<HostCard
  subtitle="Its name on the network, and its time zone."
  {readOnly}
  dirty={changed(draft, stored())}
  apply={async () => onSaved(await call(api.PUT("/host-settings/host", { body: draft })))}
  reset={() => (draft = stored())}
>
  <div class="grid gap-3 sm:grid-cols-2">
    <div class="grid gap-1">
      <label for="{id}-name" class="text-muted">Name</label>
      <input id="{id}-name" class="input" autocomplete="off" placeholder="homelab" bind:value={draft.hostname} />
    </div>
    <div class="grid gap-1">
      <label for="{id}-tz" class="text-muted">Time zone</label>
      <input id="{id}-tz" class="input" autocomplete="off" placeholder="UTC" list="{id}-zones" bind:value={draft.timezone} />
      <datalist id="{id}-zones">
        {#each zones as z (z)}<option value={z}></option>{/each}
      </datalist>
    </div>
  </div>
</HostCard>
