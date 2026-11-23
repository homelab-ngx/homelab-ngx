<script lang="ts">
  import { untrack } from "svelte";
  import { api, call, describe } from "#lib/api/client.ts";
  import HostCard from "#lib/components/host/HostCard.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { meterState, type PowerMeter, type PowerMeterTest } from "#lib/power-meter.ts";
  import { watts } from "#lib/power-profile.ts";

  interface Props {
    current: PowerMeter;
    onSaved: (m: PowerMeter) => void;
  }
  let { current, onSaved }: Props = $props();
  const id = $props.id();
  // A draft of the stored connection; the page remounts the card after an Apply.
  let url = $state(untrack(() => current.url));
  let entity = $state(untrack(() => current.entity));
  let token = $state("");
  let testing = $state(false);
  let test = $state<PowerMeterTest | null>(null);
  let testError = $state("");

  const mark = $derived(meterState(current));
  const dirty = $derived(url.trim() !== current.url || entity.trim() !== current.entity || !!token.trim());
  const problem = $derived(url.trim() && !entity.trim() ? "Enter the entity." : url.trim() && !current.has_token && !token.trim() ? "Enter a token." : "");
  const body = () => ({ url: url.trim(), entity: entity.trim(), token: token.trim() ? token.trim() : undefined, idle_only: current.idle_only });

  async function runTest() {
    testing = true;
    test = null;
    testError = "";
    try {
      test = await call<PowerMeterTest>(api.POST("/system/power-meter/test", { body: body() }));
    } catch (err) {
      testError = describe(err);
    } finally {
      testing = false;
    }
  }
</script>

<HostCard
  anchor="meter"
  title="Power meter"
  subtitle="A smart plug in Home Assistant."
  help="The daemon reads a power sensor from Home Assistant, such as the plug this host is plugged into, to show its draw at the wall. Create a long-lived access token on your Home Assistant profile page."
  readOnly={false}
  {dirty}
  {problem}
  apply={async () => {
    onSaved(await call(api.PUT("/system/power-meter", { body: body() })));
    token = "";
  }}
  reset={() => {
    url = current.url;
    entity = current.entity;
    token = "";
    test = null;
    testError = "";
  }}
>
  {#snippet status()}<Pill tone={mark.tone}>{mark.word}</Pill>{/snippet}
  {#if current.detail}<p class="text-bad">{current.detail}</p>{/if}
  <div class="grid gap-3 sm:grid-cols-2">
    <div class="grid gap-1">
      <label for="{id}-url" class="text-muted">Home Assistant address</label>
      <input id="{id}-url" class="input" type="url" bind:value={url} placeholder="http://homeassistant.local:8123" autocomplete="off" />
    </div>
    <div class="grid gap-1">
      <label for="{id}-entity" class="text-muted">Power entity</label>
      <input id="{id}-entity" class="input" bind:value={entity} placeholder="sensor.server_plug_power" autocomplete="off" spellcheck="false" />
    </div>
    <div class="grid gap-1 sm:col-span-2">
      <label for="{id}-token" class="text-muted">Access token</label>
      <input id="{id}-token" class="input" type="password" bind:value={token} placeholder={current.has_token ? "Stored" : ""} autocomplete="off" />
    </div>
  </div>
  <div class="flex flex-wrap items-center gap-3">
    <button type="button" class="btn btn-sm" disabled={testing || !url.trim() || !entity.trim() || (!current.has_token && !token.trim())} onclick={runTest}>
      {testing ? "Testing…" : "Test"}
    </button>
    {#if test?.ok}
      <span>{test.name || entity.trim()}: {test.watts === null ? "no reading" : watts(test.watts)}</span>
    {:else if test}
      <span class="text-bad">{test.error}</span>
    {:else if testError}
      <span class="text-bad">{testError}</span>
    {:else if current.state === "connected"}
      <span class="text-muted">{current.name || current.entity}: {current.watts === null ? "no reading" : watts(current.watts)}</span>
    {/if}
  </div>
</HostCard>
