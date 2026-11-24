<script lang="ts">
  import NetworkCard from "#lib/components/host/NetworkCard.svelte";
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
</script>

<HostSettingsPage
  title="LAN"
  help="The host's IPv4 address on the wired network, and the home network's subnets: a stack's port grant from the LAN lets those clients in. A wrong static address leaves the host unreachable until it is fixed at its console."
  section={(s) => s.network}
>
  {#snippet card(p)}<NetworkCard {...p} />{/snippet}
</HostSettingsPage>
