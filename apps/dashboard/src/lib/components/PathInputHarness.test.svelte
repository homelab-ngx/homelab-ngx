<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import PathInput from "./PathInput.svelte";

  let { client, onpick }: { client: QueryClient; onpick?: (p: string) => void } = $props();
  let value = $state("");
</script>

<QueryClientProvider {client}>
  <PathInput aria-label="Host folder" bind:value {onpick} />
  <output>{value}</output>
</QueryClientProvider>
