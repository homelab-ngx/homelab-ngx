<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { Draft } from "#lib/api/client.ts";
  import DraftEditor from "./DraftEditor.svelte";

  let { client, draft }: { client: QueryClient; draft: Draft } = $props();
</script>

<QueryClientProvider {client}>
  <DraftEditor {draft} />
</QueryClientProvider>
