<script lang="ts">
  import { useQueryClient } from "@tanstack/svelte-query";
  import { goto } from "$app/navigation";
  import { api, call, describe, type Schema, type Stack } from "#lib/api/client.ts";
  import { keys } from "#lib/api/queries.ts";
  import Modal from "#lib/components/Modal.svelte";

  type Draft = Schema<"Draft">;

  interface Props {
    stack: Stack;
    onClose: () => void;
  }
  let { stack, onClose }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  let name = $state("");
  let error = $state("");
  let pending = $state(false);

  const NAME = /^[a-z][a-z0-9-]{0,11}$/;
  const nameProblem = $derived(name && !NAME.test(name) ? "1-12 characters of a-z, 0-9 and -, starting with a letter." : "");
  const ready = $derived(NAME.test(name) && name !== stack.name);

  /** A new draft beside the stack, which stays on its own source. */
  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!ready) return;
    pending = true;
    error = "";
    try {
      const d = await call<Draft>(api.POST("/drafts", { body: { name, from: stack.name } }));
      void client.invalidateQueries({ queryKey: keys.all });
      onClose();
      await goto(`/stacks/drafts/${d.name}`);
    } catch (err) {
      error = describe(err);
    } finally {
      pending = false;
    }
  }
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title="Copy {stack.label || stack.name} to a new draft" description="This stack stays on its source.">
  <form id="{id}-form" class="grid gap-3" onsubmit={submit}>
    <label class="grid gap-1">
      <span class="text-muted">Draft name</span>
      <input class="input" autocomplete="off" spellcheck="false" maxlength="12" placeholder="{stack.name}-2" bind:value={name} aria-invalid={!!nameProblem} />
    </label>
    {#if nameProblem}<p class="text-warn">{nameProblem}</p>{/if}
    <p class="text-xs text-muted">Starts from the approved commit, without the repo's history.</p>
    {#if error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
  </form>
  {#snippet actions()}
    <button type="submit" form="{id}-form" class="btn btn-primary" disabled={pending || !ready}>{pending ? "Copying…" : "Copy"}</button>
  {/snippet}
</Modal>
