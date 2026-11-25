<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { Stack } from "../api/client.ts";
  import ReviewDialog from "./ReviewDialog.svelte";

  let { client, stack, onClose }: { client: QueryClient; stack: Stack | null; onClose: () => void } = $props();
</script>

<QueryClientProvider {client}>
  <ReviewDialog {stack} {onClose} />
</QueryClientProvider>
