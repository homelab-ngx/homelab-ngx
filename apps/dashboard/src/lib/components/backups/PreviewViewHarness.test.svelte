<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import PreviewView from "./PreviewView.svelte";

  let { client, has, previewing }: { client: QueryClient; has: boolean; previewing: boolean } = $props();
</script>

<QueryClientProvider {client}>
  <PreviewView backupId={1} {has} {previewing} />
</QueryClientProvider>
