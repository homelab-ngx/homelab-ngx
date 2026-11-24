<script lang="ts">
  import TunnelCard from "#lib/components/host/TunnelCard.svelte";
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
</script>

<HostSettingsPage
  title="Cloudflare Tunnel"
  help="The host keeps a connection out to Cloudflare, and Cloudflare serves each route's public name through it, so nothing at home is opened. Connect logs in at Cloudflare, where one domain (zone) is approved; the host then makes the tunnel and each route's DNS record there."
  section={(s) => s.tunnel}
>
  {#snippet card(p)}<TunnelCard {...p} />{/snippet}
</HostSettingsPage>
