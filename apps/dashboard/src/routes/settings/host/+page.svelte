<script lang="ts">
  import BasicsCard from "#lib/components/host/BasicsCard.svelte";
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
</script>

<HostSettingsPage
  title="Host"
  help="The name the host answers to on the LAN (name.local) and on a tailnet, and the time zone for logs and schedules. A change applies at once."
  section={(s) => [s.host.hostname, s.host.timezone]}
>
  {#snippet card(p)}<BasicsCard {...p} />{/snippet}
</HostSettingsPage>
