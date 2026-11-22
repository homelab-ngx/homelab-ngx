<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { Stack } from "../api/client.ts";
  import StackFolders from "./StackFolders.svelte";

  let { client, stack }: { client: QueryClient; stack: Stack } = $props();
</script>

<QueryClientProvider {client}>
  <StackFolders {stack} />
</QueryClientProvider>
