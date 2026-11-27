<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { Stack } from "../api/client.ts";
  import ResealVariablesDialog from "./ResealVariablesDialog.svelte";

  let { client, stack, onClose }: { client: QueryClient; stack: Stack; onClose: () => void } = $props();
</script>

<QueryClientProvider {client}>
  <ResealVariablesDialog {stack} {onClose} />
</QueryClientProvider>
