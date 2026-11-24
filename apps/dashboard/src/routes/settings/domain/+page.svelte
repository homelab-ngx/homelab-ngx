<script lang="ts">
  import DomainCard from "#lib/components/host/DomainCard.svelte";
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
</script>

<HostSettingsPage
  title="Domain"
  help="The private domain the dashboard and every stack route are served under, with one wildcard certificate. Point it at this host in the LAN's DNS."
  section={(s) => s.domain}
>
  {#snippet card(p)}<DomainCard {...p} />{/snippet}
</HostSettingsPage>
