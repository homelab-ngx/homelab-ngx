<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { ServiceInfo, Stack } from "../api/client.ts";
  import ServicesTable from "./ServicesTable.svelte";

  let { client, stack, services }: { client: QueryClient; stack: Stack; services: ServiceInfo[] } = $props();
</script>

<QueryClientProvider {client}>
  <ServicesTable {stack} {services} />
</QueryClientProvider>
