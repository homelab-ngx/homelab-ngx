<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { Schema } from "../../api/client.ts";
  import PoolBox from "./PoolBox.svelte";
  import SubvolumeProfileDialog from "./SubvolumeProfileDialog.svelte";

  interface Props {
    client: QueryClient;
    pool: Schema<"Pool">;
    subvolume?: Schema<"Subvolume">;
    advanced?: boolean;
    onClose?: () => void;
  }
  let { client, pool, subvolume, advanced = false, onClose = () => {} }: Props = $props();
</script>

<QueryClientProvider {client}>
  {#if subvolume}
    <SubvolumeProfileDialog {pool} {subvolume} {onClose} />
  {:else}
    <PoolBox
      {pool}
      folders={[]}
      {advanced}
      busy={false}
      mounting={false}
      onMount={() => {}}
      onAutoMount={() => {}}
      onAddDisk={() => {}}
      onRemoveDisk={() => {}}
      onUnmount={() => {}}
      onDelete={() => {}}
      onEndRemoval={() => {}}
    />
  {/if}
</QueryClientProvider>
