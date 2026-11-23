<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import HostEditor from "./HostEditor.svelte";

  let { client, root, path }: { client: QueryClient; root: string; path: string } = $props();
</script>

<QueryClientProvider {client}>
  <HostEditor {root} {path} />
</QueryClientProvider>
