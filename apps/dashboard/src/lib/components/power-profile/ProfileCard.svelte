<script lang="ts">
  import { untrack } from "svelte";
  import { api, call } from "#lib/api/client.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import HostCard from "#lib/components/host/HostCard.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { appliedState, PROFILES, TUNABLES, tunableRow, type PowerProfileStatus } from "#lib/power-profile.ts";

  interface Props {
    current: PowerProfileStatus;
    onSaved: (s: PowerProfileStatus) => void;
  }
  let { current, onSaved }: Props = $props();
  const id = $props.id();
  // A draft of the stored choice; the page remounts the card after an Apply.
  let profile = $state(untrack(() => current.profile));
  let governor = $state(untrack(() => current.governor));
  const applied = $derived(appliedState(current));
  const refused = $derived(current.tunables.filter((t) => t.refused > 0).length);
  const set = $derived(current.tunables.filter((t) => t.state === "set").length);
</script>

<HostCard
  anchor="profile"
  title="Profile"
  subtitle="How far the host goes to save power."
  help="Power saver turns on PCIe link sleep on every link and autosuspend on every USB device; some network cards and USB sticks drop out under it. Balanced keeps the safe savings. Performance holds the CPU at full speed."
  readOnly={current.read_only}
  dirty={profile !== current.profile || governor !== current.governor}
  apply={async () => onSaved(await call(api.PUT("/system/power-profile", { body: { profile, governor } })))}
  reset={() => {
    profile = current.profile;
    governor = current.governor;
  }}
>
  {#snippet status()}<Pill tone={applied.tone}>{applied.word}</Pill>{/snippet}
  <fieldset class="grid gap-1">
    <legend class="sr-only">Profile</legend>
    <div class="flex flex-wrap gap-2">
      {#each PROFILES as p (p.value)}
        <label class="flex cursor-pointer items-start gap-2 rounded-md border px-3 py-1.5 {profile === p.value ? 'border-accent bg-accent-soft' : 'border-line'}">
          <input type="radio" class="mt-0.5 accent-accent" name="{id}-profile" value={p.value} bind:group={profile} />
          <span>{p.label} <span class="block text-xs text-muted">{p.note}</span></span>
        </label>
      {/each}
    </div>
  </fieldset>
  {#if current.governors.length}
    <div class="grid gap-1">
      <label for="{id}-gov" class="text-muted">CPU governor (optional)</label>
      <select id="{id}-gov" class="input sm:max-w-xs" bind:value={governor}>
        <option value="">The profile's</option>
        {#each current.governors as g (g)}<option value={g}>{g}</option>{/each}
      </select>
    </div>
  {/if}
  {#if current.tunables.length}
    <Disclosure label="Tunables" summary={refused ? `${set} set, ${refused} refused` : `${set} set`}>
      <ul class="mt-2 grid gap-1">
        {#each current.tunables as t (t.name)}
          {@const row = tunableRow(t)}
          <li class="grid grid-cols-[minmax(0,10rem)_6rem_minmax(0,1fr)] items-baseline gap-x-3 max-sm:grid-cols-[minmax(0,1fr)_auto]">
            <span>{TUNABLES[t.name]}</span>
            <Pill tone={row.tone}>{row.word}</Pill>
            <span class="truncate text-muted max-sm:col-span-2" title={row.value}>{row.value}</span>
          </li>
        {/each}
      </ul>
    </Disclosure>
  {/if}
</HostCard>
