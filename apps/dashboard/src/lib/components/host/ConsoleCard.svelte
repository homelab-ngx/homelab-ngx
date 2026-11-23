<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { untrack } from "svelte";
  import { api, call } from "#lib/api/client.ts";
  import { hostKeys, type HostSettings } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  let { settings, readOnly, onSaved }: Props = $props();
  const id = $props.id();
  let keyboard = $state(untrack(() => settings.console.keyboard ?? ""));

  const layouts = createQuery(() => ({
    queryKey: hostKeys.keyboardLayouts,
    queryFn: () => call(api.GET("/host-settings/keyboard-layouts")),
    staleTime: Infinity,
  }));
</script>

<HostCard
  title="Console"
  subtitle="The keyboard plugged into the host."
  help="The layout the host's own keyboard types in at its screen. Applying restarts the console, which logs root in again."
  {readOnly}
  dirty={keyboard !== (settings.console.keyboard ?? "")}
  apply={async () => onSaved(await call(api.PUT("/host-settings/console", { body: { keyboard } })))}
  reset={() => (keyboard = settings.console.keyboard ?? "")}
>
  <div class="grid gap-1 sm:max-w-xs">
    <label for="{id}-kb" class="text-muted">Keyboard layout</label>
    <select id="{id}-kb" class="input" bind:value={keyboard}>
      <option value="">English (US)</option>
      {#each layouts.data?.layouts ?? [] as l (l.name)}
        {#if l.name !== "us"}<option value={l.name}>{l.description}</option>{/if}
      {/each}
      {#if keyboard && !layouts.data}<option value={keyboard}>{keyboard}</option>{/if}
    </select>
  </div>
</HostCard>
