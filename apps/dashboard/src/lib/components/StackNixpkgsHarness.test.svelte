<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { Stack } from "../api/client.ts";
  import StackNixpkgs from "./StackNixpkgs.svelte";

  let { client, stack }: { client: QueryClient; stack: Stack } = $props();
</script>

<QueryClientProvider {client}>
  <StackNixpkgs {stack} />
</QueryClientProvider>
