<script lang="ts">
  import ConsoleCard from "#lib/components/host/ConsoleCard.svelte";
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
</script>

<HostSettingsPage
  title="Console"
  help="The layout the host's own keyboard types in at its screen. Applying restarts the console, which logs root in again."
  section={(s) => s.console}
>
  {#snippet card(p)}<ConsoleCard {...p} />{/snippet}
</HostSettingsPage>
