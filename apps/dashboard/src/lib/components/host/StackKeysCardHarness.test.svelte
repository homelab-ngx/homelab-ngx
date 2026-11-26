<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import StackKeysCard from "./StackKeysCard.svelte";

  let { client }: { client: QueryClient } = $props();
</script>

<QueryClientProvider {client}>
  <StackKeysCard />
</QueryClientProvider>
