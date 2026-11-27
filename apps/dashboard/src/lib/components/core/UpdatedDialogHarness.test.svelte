<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import UpdatedDialog from "./UpdatedDialog.svelte";

  let { client }: { client: QueryClient } = $props();
</script>

<QueryClientProvider {client}>
  <UpdatedDialog />
</QueryClientProvider>
