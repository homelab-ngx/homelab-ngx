<script lang="ts">
  import { useQueryClient } from "@tanstack/svelte-query";
  import { untrack } from "svelte";
  import { api, call, describe, type Stack } from "#lib/api/client.ts";
  import { keys } from "#lib/api/queries.ts";
  import Modal from "#lib/components/Modal.svelte";
  import { MAX_STACK_LABEL } from "#lib/stacks.ts";

  interface Props {
    stack: Stack;
    onClose: () => void;
  }
  let { stack, onClose }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  /** The editable copy; the dialog is mounted anew each time it opens. */
  let draft = $state(untrack(() => stack.label));
  let error = $state("");
  let pending = $state(false);

  const tooLong = $derived(draft.trim().length > MAX_STACK_LABEL);

  async function save(label: string) {
    pending = true;
    error = "";
    try {
      const s = await call<Stack>(api.PATCH("/stacks/{name}", { params: { path: { name: stack.name } }, body: { label } }));
      client.setQueryData([...keys.stacks, stack.name], s);
      void client.invalidateQueries({ queryKey: keys.stacks });
      onClose();
    } catch (e) {
      error = describe(e);
    } finally {
      pending = false;
    }
  }

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!tooLong) void save(draft);
  }
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title="Rename {stack.label || stack.name}">
  <form id="{id}-form" class="grid gap-1" onsubmit={submit}>
    <label for="{id}-label" class="text-muted">Dashboard name</label>
    <input id="{id}-label" class="input" autocomplete="off" placeholder={stack.name} bind:value={draft} aria-invalid={tooLong} />
    {#if tooLong}
      <p class="text-bad">At most {MAX_STACK_LABEL} characters.</p>
    {/if}
    <p class="mt-1 text-xs text-muted">Services, folders and logs keep the name {stack.name}.</p>
    {#if error}<p role="alert" class="mt-2 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
  </form>
  {#snippet actions()}
    {#if stack.label}
      <button type="button" class="btn" disabled={pending} onclick={() => save("")}>Reset to {stack.name}</button>
    {/if}
    <button type="submit" form="{id}-form" class="btn btn-primary" disabled={pending || tooLong}>Save</button>
  {/snippet}
</Modal>
