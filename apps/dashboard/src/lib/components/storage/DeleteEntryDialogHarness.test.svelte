<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import DeleteEntryDialog from "./DeleteEntryDialog.svelte";

  let { client, path, type, onDeleted }: { client: QueryClient; path: string; type: "dir" | "file" | "symlink"; onDeleted: () => void } = $props();
</script>

<QueryClientProvider {client}>
  <DeleteEntryDialog {path} {type} onClose={() => {}} {onDeleted} />
</QueryClientProvider>
