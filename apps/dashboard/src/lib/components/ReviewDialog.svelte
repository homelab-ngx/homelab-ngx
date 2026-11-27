<script lang="ts">
  import { createMutation, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Stack } from "#lib/api/client.ts";
  import { keys } from "#lib/api/queries.ts";
  import { shortRev } from "#lib/format.ts";
  import { removedGrants, stackTitle } from "#lib/stacks.ts";
  import { missingVariables } from "#lib/variables.ts";
  import Modal from "./Modal.svelte";

  interface Props {
    /** The stack whose fetched commit to review; null closes the dialog. */
    stack: Stack | null;
    onClose: () => void;
  }
  let { stack, onClose }: Props = $props();

  const client = useQueryClient();
  let error = $state("");

  /** Approve the fetched commit, then update to it if the stack is installed. */
  const approve = createMutation(() => ({
    mutationFn: async (s: Stack) => {
      await call(api.POST("/stacks/{name}/approve", { params: { path: { name: s.name } }, body: { rev: s.fetched.rev } }));
      if (s.generation > 0) await call(api.POST("/stacks/{name}/update", { params: { path: { name: s.name } } }));
    },
    onMutate: () => (error = ""),
    onSuccess: () => onClose(),
    onError: (e: unknown) => (error = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: keys.all }),
  }));
</script>

{#if stack}
  {@const s = stack}
  {@const removed = removedGrants(s)}
  <Modal
    open={true}
    onOpenChange={(o) => !o && onClose()}
    title="Update {stackTitle(s)} to {shortRev(s.fetched.rev)}?"
    description="Nothing is built before approval."
  >
    <div class="flex flex-col gap-4">
      {#if s.commits.length > 0}
        <section aria-labelledby="review-commits">
          <h3 id="review-commits" class="label">{s.commits.length} new commit{s.commits.length === 1 ? "" : "s"}</h3>
          <ul class="mt-1.5 text-xs">
            {#each s.commits as c (c.rev)}
              <li class="truncate py-0.5"><span class="text-muted">{shortRev(c.rev)}</span> {c.subject}</li>
            {/each}
          </ul>
        </section>
      {/if}
      <section aria-labelledby="review-access">
        <h3 id="review-access" class="label">Access changes</h3>
        <ul class="mt-1.5 text-xs">
          {#each s.new_grants as g (g.kind + g.detail)}<li class="py-0.5 text-ok">+ {g.kind} {g.detail}</li>{/each}
          {#each removed as g (g.kind + g.detail)}<li class="py-0.5 text-bad">− {g.kind} {g.detail}</li>{/each}
        </ul>
        <p class="mt-1.5 text-xs text-muted">Anything not listed stays blocked.</p>
      </section>
      {#if !s.variables_ready}
        <p class="rounded-md bg-warn-soft px-3 py-2 text-warn">
          {#if s.variables_key_mismatch}
            Variables are sealed to another host's key.
          {:else if s.variables_error}
            {s.variables_error}
          {:else}
            Needs values first: {missingVariables(s).join(", ") || "its variables"}.
          {/if}
          <a class="underline" href="/stacks/{s.name}/configuration" onclick={onClose}>Open configuration</a>
        </p>
      {/if}
      {#if error}
        <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
      {/if}
    </div>
    {#snippet actions()}
      <button
        type="button"
        class="btn btn-primary"
        disabled={approve.isPending || !s.variables_ready}
        title={!s.variables_ready ? "Set required values in Configuration first" : undefined}
        onclick={() => approve.mutate(s)}
      >
        {approve.isPending ? "Approving…" : s.generation > 0 ? "Approve and update" : "Approve"}
      </button>
    {/snippet}
  </Modal>
{/if}
