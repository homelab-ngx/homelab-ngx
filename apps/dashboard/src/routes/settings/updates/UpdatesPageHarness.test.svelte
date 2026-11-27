<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import UpdatesPage from "./+page.svelte";

  let { client }: { client: QueryClient } = $props();
</script>

<QueryClientProvider {client}>
  <UpdatesPage />
</QueryClientProvider>
