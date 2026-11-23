<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";
  import { bytes } from "#lib/format.ts";

  interface Props {
    /** The host path to delete. */
    path: string;
    type: Schema<"Entry">["type"];
    /** The space deleting it returns, when it was measured. */
    freed?: number | null;
    onClose: () => void;
    onDeleted?: () => void;
  }
  let { path, type, freed = null, onClose, onDeleted }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  const name = $derived(path.slice(path.lastIndexOf("/") + 1));
  const folder = $derived(type === "dir");
  let typed = $state("");
  let error = $state("");

  // A folder that holds anything is deleted with all of it, so its name is typed first.
  const inside = createQuery(() => ({
    queryKey: ["daemon", "storage", "browse", path],
    queryFn: () => call<Schema<"Listing">>(api.GET("/storage/browse", { params: { query: { path } } })),
    enabled: folder,
    retry: false,
  }));
  const items = $derived(folder ? inside.data?.total : 0);
  const ready = $derived(items === 0 || (items !== undefined && typed === name));

  const remove = createMutation(() => ({
    mutationFn: () => call(api.DELETE("/storage/entry", { params: { query: { path, confirm: items ? typed : undefined } } })),
    onMutate: () => (error = ""),
    onSuccess: () => {
      onDeleted?.();
      onClose();
    },
    onError: (e: unknown) => (error = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: ["daemon", "storage"] }),
  }));

  const what = $derived(folder ? "folder" : type === "symlink" ? "link" : "file");
  const description = $derived.by(() => {
    if (type === "symlink") return "Only the link goes. What it points to stays.";
    const freeing = freed ? ` Frees ${bytes(freed)}.` : "";
    if (!folder) return `Deleted for good.${freeing}`;
    if (items === undefined) return "Deleted for good, with everything in it.";
    return items ? `Deleted for good, with its ${items.toLocaleString()} item${items === 1 ? "" : "s"}.${freeing}` : "The folder is empty.";
  });

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (ready) remove.mutate();
  }
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title="Delete {name}?" {description}>
  <form id="{id}-form" class="grid gap-3" onsubmit={submit}>
    <p class="text-muted [overflow-wrap:anywhere]">{path}</p>
    {#if inside.error}
      <p role="alert" class="text-bad">{describe(inside.error)}</p>
    {:else if items}
      <div class="grid gap-1">
        <label for="{id}-typed" class="text-muted">Type {name}</label>
        <input id="{id}-typed" class="input" autocomplete="off" spellcheck="false" bind:value={typed} />
      </div>
    {/if}
    {#if error}<p role="alert" class="text-bad">{error}</p>{/if}
  </form>
  {#snippet actions()}
    <button type="submit" form="{id}-form" class="btn btn-danger" disabled={!ready || remove.isPending}>Delete {what}</button>
  {/snippet}
</Modal>
