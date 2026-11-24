<script lang="ts">
  import SshCard from "#lib/components/host/SshCard.svelte";
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
</script>

<HostSettingsPage
  title="SSH"
  help="Public keys that may log in as root over SSH, one per line. Passwords are never accepted. Without a key, only the host's own screen gives a shell."
  section={(s) => s.host.ssh_keys}
>
  {#snippet card(p)}<SshCard {...p} />{/snippet}
</HostSettingsPage>
