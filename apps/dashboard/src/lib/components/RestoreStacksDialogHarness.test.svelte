<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import RestoreStacksDialog from "./RestoreStacksDialog.svelte";

  let { client, onClose }: { client: QueryClient; onClose: () => void } = $props();
</script>

<QueryClientProvider {client}>
  <RestoreStacksDialog {onClose} />
</QueryClientProvider>
