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
  const stored = () => (settings.host.ssh_keys ?? []).join("\n");
  let text = $state(untrack(stored));
  const keys = $derived(
    text
      .split("\n")
      .map((k) => k.trim())
      .filter(Boolean),
  );
</script>

<HostCard
  title="SSH"
  help="Public keys that may log in as root over SSH, one per line. Passwords are never accepted. Without a key, the host has no shell login."
  {readOnly}
  dirty={changed(keys, settings.host.ssh_keys ?? [])}
  apply={async () => onSaved(await call(api.PUT("/host-settings/ssh", { body: { keys } })))}
  reset={() => (text = stored())}
>
  <div class="grid gap-1">
    <label for="{id}-keys" class="text-muted">Public keys</label>
    <textarea id="{id}-keys" class="input min-h-24" autocomplete="off" spellcheck="false" placeholder="ssh-ed25519 AAAA… user@laptop" bind:value={text}></textarea>
  </div>
</HostCard>
