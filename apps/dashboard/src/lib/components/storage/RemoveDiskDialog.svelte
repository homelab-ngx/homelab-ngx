<script lang="ts">
  import { createMutation, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";
  import { removeBlocked } from "#lib/storage.ts";

  interface Props {
    pool: Schema<"Pool">;
    disk: Schema<"Disk">;
    onClose: () => void;
  }
  let { pool, disk, onClose }: Props = $props();

  const client = useQueryClient();
  let error = $state("");
  const blocked = $derived(removeBlocked(pool, disk));

  const remove = createMutation(() => ({
    mutationFn: () =>
      call(api.DELETE("/storage/pools/{uuid}/disks", { params: { path: { uuid: pool.uuid }, query: { disk: disk.path } } })),
    onMutate: () => (error = ""),
    onSuccess: onClose,
    onError: (e: unknown) => (error = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: ["daemon", "storage"] }),
  }));
</script>

<Modal
  open={true}
  onOpenChange={(o) => !o && onClose()}
  title="Remove {disk.name} from {pool.name}?"
  description="Its files move to the other disks first, which can take hours. Then it is erased."
>
  {#if blocked}<p class="text-muted">{blocked}</p>{/if}
  {#if error}<p role="alert" class="text-bad">{error}</p>{/if}
  {#snippet actions()}
    <button type="button" class="btn btn-danger" title={blocked} disabled={!!blocked || remove.isPending} onclick={() => remove.mutate()}>
      Remove disk
    </button>
  {/snippet}
</Modal>
