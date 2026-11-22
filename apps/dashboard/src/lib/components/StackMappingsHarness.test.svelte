<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { Stack } from "../api/client.ts";
  import StackMappings from "./StackMappings.svelte";

  let { client, stack }: { client: QueryClient; stack: Stack } = $props();
</script>

<QueryClientProvider {client}>
  <StackMappings {stack} />
</QueryClientProvider>
