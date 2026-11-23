<script lang="ts">
  import { untrack } from "svelte";
  import { api, call } from "#lib/api/client.ts";
  import type { Clock } from "#lib/clock.svelte.ts";
  import Pill from "#lib/components/Pill.svelte";
  import { changed, joinList, splitList, type HostSettings } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  let { settings, readOnly, onSaved }: Props = $props();
  const id = $props.id();
  let format = $state<Clock>(untrack(() => settings.time.clock ?? ""));
  let ntp = $state(untrack(() => joinList(settings.time.ntp)));
  const sync = $derived(settings.time_sync);

  // Each choice shows a quarter to four in its own form.
  const sample = (hourCycle?: "h12" | "h23") => new Date(2000, 0, 1, 15, 45).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit", hourCycle });
  const FORMATS: { value: Clock; label: string; example: string }[] = [
    { value: "", label: "Automatic", example: `${sample()} here` },
    { value: "12h", label: "12-hour", example: sample("h12") },
    { value: "24h", label: "24-hour", example: sample("h23") },
  ];
</script>

<HostCard
  title="Clock"
  subtitle="How times read, and where the host gets the time."
  help="The format of every time the dashboard shows; Automatic follows each browser's language. The host sets its clock from the time servers: Arch's public pool unless others are named, such as the router for a host without internet."
  {readOnly}
  dirty={format !== (settings.time.clock ?? "") || changed(splitList(ntp), settings.time.ntp ?? [])}
  apply={async () => onSaved(await call(api.PUT("/host-settings/time", { body: { clock: format, ntp: splitList(ntp) } })))}
  reset={() => {
    format = settings.time.clock ?? "";
    ntp = joinList(settings.time.ntp);
  }}
>
  {#snippet status()}<Pill tone={sync.synced ? "ok" : "warn"}>{sync.synced ? "synced" : "not synced"}</Pill>{/snippet}
  <div class="grid gap-4">
    <fieldset class="grid gap-1">
      <legend class="mb-1 text-muted">Format</legend>
      <div class="flex flex-wrap gap-2">
        {#each FORMATS as f (f.value)}
          <label class="flex cursor-pointer items-start gap-2 rounded-md border px-3 py-1.5 {format === f.value ? 'border-accent bg-accent-soft' : 'border-line'}">
            <input type="radio" class="mt-0.5 accent-accent" name="{id}-format" value={f.value} bind:group={format} />
            <span>{f.label} <span class="block text-xs text-muted">{f.example}</span></span>
          </label>
        {/each}
      </div>
    </fieldset>
    <div class="grid gap-1">
      <label for="{id}-ntp" class="text-muted">Time servers</label>
      <input id="{id}-ntp" class="input" autocomplete="off" placeholder="Arch's pool" bind:value={ntp} />
      {#if sync.server}<p class="text-xs text-muted">Set from {sync.server}</p>{/if}
    </div>
  </div>
</HostCard>
