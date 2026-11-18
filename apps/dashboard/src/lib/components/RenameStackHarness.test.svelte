<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { Stack } from "../api/client.ts";
  import RenameStack from "./RenameStack.svelte";

  let { client, stack, onClose }: { client: QueryClient; stack: Stack; onClose: () => void } = $props();
</script>

<QueryClientProvider {client}>
  <RenameStack {stack} {onClose} />
</QueryClientProvider>
