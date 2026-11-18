<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { ServiceInfo, Stack } from "../api/client.ts";
  import StackTable from "./StackTable.svelte";

  let { client, stacks, services }: { client: QueryClient; stacks: Stack[]; services: ServiceInfo[] } = $props();
</script>

<QueryClientProvider {client}>
  <StackTable {stacks} {services} />
</QueryClientProvider>
