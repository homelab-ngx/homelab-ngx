<script lang="ts">
  import FirewallCard from "#lib/components/host/FirewallCard.svelte";
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
</script>

<HostSettingsPage
  title="Firewall"
  help="Ports let in besides SSH, HTTP, HTTPS and the VPN, which are always open. Everything else from outside is dropped. A stack's own ports are its grants."
  section={(s) => s.firewall}
>
  {#snippet card(p)}<FirewallCard {...p} />{/snippet}
</HostSettingsPage>
