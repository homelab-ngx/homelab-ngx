<script lang="ts">
  import TailscaleCard from "#lib/components/host/TailscaleCard.svelte";
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
</script>

<HostSettingsPage
  title="Tailscale"
  help="Puts the host on a Tailscale network (a tailnet), so its devices reach the host by name with no port opened at home. Log in opens Tailscale's page, where the host is approved from any device. Offered subnets let the tailnet reach the LAN through the host, once approved in the tailnet's admin console."
  section={(s) => s.tailscale}
>
  {#snippet card(p)}<TailscaleCard {...p} />{/snippet}
</HostSettingsPage>
