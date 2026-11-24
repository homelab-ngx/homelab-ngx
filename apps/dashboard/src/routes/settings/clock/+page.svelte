<script lang="ts">
  import ClockCard from "#lib/components/host/ClockCard.svelte";
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
</script>

<HostSettingsPage
  title="Clock"
  help="The format of every time the dashboard shows; Automatic follows each browser's language. The host sets its clock from the time servers: Arch's public pool unless others are named, such as the router for a host without internet."
  section={(s) => s.time}
>
  {#snippet card(p)}<ClockCard {...p} />{/snippet}
</HostSettingsPage>
