<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { Schema, Stack } from "#lib/api/client.ts";
  import type { Buffer } from "#lib/drafts.ts";
  import StackSource from "./StackSource.svelte";

  let {
    client,
    stack,
    files,
    onUnsaved,
  }: { client: QueryClient; stack: Stack; files: Schema<"StackFiles">; onUnsaved?: (b: Buffer, e: string) => void } = $props();
</script>

<QueryClientProvider {client}>
  <StackSource {stack} {files} {onUnsaved} />
</QueryClientProvider>
