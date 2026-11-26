<script lang="ts">
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
  import SshCard from "#lib/components/host/SshCard.svelte";
  import StackKeysCard from "#lib/components/host/StackKeysCard.svelte";
</script>

<HostSettingsPage
  title="SSH"
  help="Public keys that may log in as root over SSH, one per line. Passwords are never accepted. Without a key, only the host's own screen gives a shell."
  section={(s) => s.host.ssh_keys}
>
  {#snippet card(p)}<SshCard {...p} />{/snippet}
</HostSettingsPage>

<div class="mt-4 max-w-4xl">
  <StackKeysCard />
</div>
