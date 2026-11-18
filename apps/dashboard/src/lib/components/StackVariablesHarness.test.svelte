<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { Stack } from "../api/client.ts";
  import StackVariables from "./StackVariables.svelte";

  let { client, stack }: { client: QueryClient; stack: Stack } = $props();
</script>

<QueryClientProvider {client}>
  <StackVariables {stack} />
</QueryClientProvider>
