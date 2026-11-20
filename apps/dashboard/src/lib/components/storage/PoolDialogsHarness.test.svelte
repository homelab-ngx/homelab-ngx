<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { Schema } from "../../api/client.ts";
  import CreatePoolDialog from "./CreatePoolDialog.svelte";
  import DeletePoolDialog from "./DeletePoolDialog.svelte";

  interface Props {
    client: QueryClient;
    spare?: Schema<"Disk">[];
    pool?: Schema<"Pool">;
    users?: string[];
    onClose: () => void;
  }
  let { client, spare, pool, users = [], onClose }: Props = $props();
</script>

<QueryClientProvider {client}>
  {#if pool}
    <DeletePoolDialog {pool} {users} {onClose} />
  {:else}
    <CreatePoolDialog spare={spare ?? []} taken={["tank"]} {onClose} />
  {/if}
</QueryClientProvider>
