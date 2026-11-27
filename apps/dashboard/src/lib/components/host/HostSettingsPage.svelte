<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import type { Snippet } from "svelte";
  import { describe } from "#lib/api/client.ts";
  import SettingsHead from "#lib/components/settings/SettingsHead.svelte";
  import { fetchHostSettings, hostKeys, type HostSettings } from "#lib/host.ts";
  import { settingsStatusKey } from "#lib/settings.ts";
  import AppliedStatus from "./AppliedStatus.svelte";

  interface CardProps {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  interface Props {
    title: string;
    help: string;
    /** The part of the settings the card edits: a change to it mounts the card afresh. */
    section: (s: HostSettings) => unknown;
    actions?: Snippet<[CardProps]>;
    above?: Snippet<[CardProps]>;
    card: Snippet<[CardProps]>;
    below?: Snippet<[CardProps]>;
  }
  let { title, help: helpText, section, actions: headActions, above, card, below }: Props = $props();

  const client = useQueryClient();
  const settings = createQuery(() => ({
    queryKey: hostKeys.settings,
    queryFn: fetchHostSettings,
    // Dynamic DNS reports its first check a moment after it is turned on.
    refetchInterval: (q: { state: { data?: HostSettings } }) => (q.state.data?.ddns.enabled && !q.state.data.ddns.status ? 3000 : false),
  }));
  const s = $derived(settings.data);
  const readOnly = $derived(s?.managed === "nixos");
  function onSaved(next: HostSettings) {
    client.setQueryData(hostKeys.settings, next);
    void client.invalidateQueries({ queryKey: settingsStatusKey });
  }
</script>

<!-- One section of the host's settings on a page of its own: the head, how the last apply went, and the card. -->
<SettingsHead {title}>
  {#snippet help()}{helpText}{/snippet}
  {#snippet actions()}
    {#if s && headActions}{@render headActions({ settings: s, readOnly, onSaved })}{/if}
  {/snippet}
</SettingsHead>

{#if settings.error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(settings.error)}</p>
{:else if s}
  <AppliedStatus settings={s} />
  {#if above}
    <div class="mt-4 max-w-4xl empty:hidden">{@render above({ settings: s, readOnly, onSaved })}</div>
  {/if}
  <!-- Keyed by the stored section: an Apply mounts the card afresh on the new values. -->
  <div class="mt-5 max-w-4xl">
    {#key JSON.stringify(section(s))}{@render card({ settings: s, readOnly, onSaved })}{/key}
  </div>
  {#if below}
    <div class="mt-4 max-w-4xl empty:hidden">{@render below({ settings: s, readOnly, onSaved })}</div>
  {/if}
{:else}
  <p class="mt-4 text-muted">Loading…</p>
{/if}
