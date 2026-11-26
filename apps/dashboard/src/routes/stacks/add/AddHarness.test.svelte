<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import AddPage from "./+page.svelte";

  let { client }: { client: QueryClient } = $props();
</script>

<QueryClientProvider {client}>
  <AddPage />
</QueryClientProvider>
