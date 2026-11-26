<script lang="ts">
  import HostSettingsPage from "#lib/components/host/HostSettingsPage.svelte";
  import MailCard from "#lib/components/host/MailCard.svelte";
</script>

<HostSettingsPage
  title="Mail relay"
  help="Outbound SMTP relay for the host and stacks on port 25."
  section={(s) => s.smtp}
>
  {#snippet card(p)}<MailCard {...p} />{/snippet}
</HostSettingsPage>
