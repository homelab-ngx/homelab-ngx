<script lang="ts">
  import { createMutation, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";
  import DiskLine from "./DiskLine.svelte";

  interface Props {
    pool: Schema<"Pool">;
    spare: Schema<"Disk">[];
    onClose: () => void;
  }
  let { pool, spare, onClose }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  let path = $state("");
  let typed = $state("");
  let error = $state("");

  const disk = $derived(spare.find((d) => d.path === path));
  const ready = $derived(!!disk && (!disk.contents || typed === pool.name));

  const add = createMutation(() => ({
    mutationFn: () =>
      call(
        api.POST("/storage/pools/{uuid}/disks", {
          params: { path: { uuid: pool.uuid } },
          body: { disk: path, confirm: disk?.contents ? typed : undefined },
        }),
      ),
    onMutate: () => (error = ""),
    onSuccess: onClose,
    onError: (e: unknown) => (error = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: ["daemon", "storage"] }),
  }));

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (ready) add.mutate();
  }
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title="Add disk to {pool.name}">
  <form id="{id}-form" class="grid gap-4" onsubmit={submit}>
    {#if spare.length === 0}
      <p class="text-muted">No spare disk. Plug one in, or unmount its partitions.</p>
    {:else}
      <fieldset class="grid gap-1">
        <legend class="sr-only">Disk</legend>
        {#each spare as d (d.path)}
          <label class="flex items-baseline gap-2.5 rounded-md border border-line px-3 py-2 has-checked:border-accent">
            <input type="radio" name="{id}-disk" value={d.path} bind:group={path} />
            <DiskLine disk={d} />
          </label>
        {/each}
      </fieldset>
    {/if}
    {#if disk?.contents}
      <div class="grid gap-1">
        <p class="text-warn">Everything on {disk.name} is erased.</p>
        <label for="{id}-typed" class="text-muted">Type {pool.name}</label>
        <input id="{id}-typed" class="input" autocomplete="off" spellcheck="false" bind:value={typed} />
      </div>
    {/if}
    {#if error}<p role="alert" class="text-bad">{error}</p>{/if}
  </form>
  {#snippet actions()}
    <button type="submit" form="{id}-form" class="btn btn-primary" disabled={!ready || add.isPending}>
      {add.isPending ? "Adding…" : "Add disk"}
    </button>
  {/snippet}
</Modal>
