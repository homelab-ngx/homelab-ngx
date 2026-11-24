<script lang="ts">
  import VpnCard from "#lib/components/host/VpnCard.svelte";
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
</script>

<HostSettingsPage
  title="WireGuard VPN"
  help="A WireGuard server on the host. Each device has its own key and an address in the VPN, and reaches the host through the router from anywhere. Add a device makes its keys and shows its config once, as a QR code for the WireGuard app."
  section={(s) => [s.wireguard, s.ddns.enabled, s.ddns.name]}
>
  {#snippet card(p)}<VpnCard {...p} />{/snippet}
</HostSettingsPage>
