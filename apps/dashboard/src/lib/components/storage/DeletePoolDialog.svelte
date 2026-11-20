<script lang="ts">
  import { createMutation, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";

  interface Props {
    pool: Schema<"Pool">;
    /** The stacks that keep a folder on the pool, by title. */
    users: string[];
    onClose: () => void;
  }
  let { pool, users, onClose }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  let typed = $state("");
  let error = $state("");
  const ready = $derived(users.length === 0 && typed === pool.name);

  const remove = createMutation(() => ({
    mutationFn: () =>
      call(api.DELETE("/storage/pools/{uuid}", { params: { path: { uuid: pool.uuid }, query: { confirm: typed } } })),
    onMutate: () => (error = ""),
    onSuccess: onClose,
    onError: (e: unknown) => (error = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: ["daemon", "storage"] }),
  }));

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (ready) remove.mutate();
  }
</script>

<Modal
  open={true}
  onOpenChange={(o) => !o && onClose()}
  title="Delete {pool.name}?"
  description="Every file on the pool is gone. Its {pool.disks.length} disks are erased and become spare."
>
  <form id="{id}-form" class="grid gap-3" onsubmit={submit}>
    {#if users.length > 0}
      <p class="text-muted">Used by {users.join(", ")}. Move or remove those stacks first.</p>
    {:else}
      <div class="grid gap-1">
        <label for="{id}-typed" class="text-muted">Type {pool.name}</label>
        <input id="{id}-typed" class="input" autocomplete="off" spellcheck="false" bind:value={typed} />
      </div>
    {/if}
    {#if error}<p role="alert" class="text-bad">{error}</p>{/if}
  </form>
  {#snippet actions()}
    <button type="submit" form="{id}-form" class="btn btn-danger" disabled={!ready || remove.isPending}>Delete pool</button>
  {/snippet}
</Modal>
