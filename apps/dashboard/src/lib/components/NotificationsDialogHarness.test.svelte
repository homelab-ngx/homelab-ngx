<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import NotificationsDialog from "./NotificationsDialog.svelte";
  import type { Snapshot } from "#lib/notifications.ts";

  let { client, snapshot, onClose }: { client: QueryClient; snapshot: Snapshot | undefined; onClose: () => void } = $props();
</script>

<QueryClientProvider {client}>
  <NotificationsDialog {snapshot} {onClose} />
</QueryClientProvider>
