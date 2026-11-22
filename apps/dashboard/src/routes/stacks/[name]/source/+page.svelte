<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { page } from "$app/state";
  import { describe } from "#lib/api/client.ts";
  import { busy, busyPoll, fetchDraft, fetchStack, fetchStackFiles, idlePoll, keys } from "#lib/api/queries.ts";
  import DraftEditor from "#lib/components/drafts/DraftEditor.svelte";
  import StackSource from "#lib/components/drafts/StackSource.svelte";
  import { building, type Buffer } from "#lib/drafts.ts";
  import { draftOf, stackTitle } from "#lib/stacks.ts";

  const name = $derived(page.params.name ?? "");
  const stack = createQuery(() => ({
    queryKey: [...keys.stacks, name],
    queryFn: () => fetchStack(name),
    refetchInterval: (q) => (q.state.data && busy(q.state.data) ? busyPoll : idlePoll),
  }));

  /** A stack that runs from a draft is edited here; any other is read from its commit. */
  const draftName = $derived(stack.data ? draftOf(stack.data.source) : null);
  const draft = createQuery(() => ({
    queryKey: keys.draft(draftName ?? ""),
    queryFn: () => fetchDraft(draftName!),
    enabled: draftName !== null,
    refetchInterval: (q) => (building(q.state.data) ? busyPoll : false),
  }));
  const rev = $derived(stack.data?.approved?.rev ?? stack.data?.fetched?.rev ?? "");
  const files = createQuery(() => ({
    queryKey: keys.stackFiles(name, rev),
    queryFn: () => fetchStackFiles(name),
    enabled: !!stack.data && draftName === null && rev !== "",
    staleTime: Infinity,
  }));

  /** Edits a fork did not save: the draft's editor starts from them. */
  let carried = $state<{ buffer: Buffer; error: string } | null>(null);
</script>

<svelte:head><title>Source code · {stack.data ? stackTitle(stack.data) : name} · Homelab-ngx</title></svelte:head>

{#if stack.error}
  <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(stack.error)}</p>
{:else if draftName}
  {#if draft.error}
    <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(draft.error)}</p>
  {:else if draft.data}
    <!-- The editor keeps its own unsaved copy of the files, made once per draft. -->
    {#key draftName}
      <DraftEditor draft={draft.data} embedded initial={carried?.buffer} initialError={carried?.error} />
    {/key}
  {:else}
    <p class="text-muted">Loading…</p>
  {/if}
{:else if files.error}
  <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(files.error)}</p>
{:else if stack.data && files.data}
  {#key rev}
    <StackSource stack={stack.data} files={files.data} onUnsaved={(buffer, error) => (carried = { buffer, error })} />
  {/key}
{:else if stack.data && rev === ""}
  <p class="rounded-lg border border-line bg-surface px-4 py-3 text-muted">Nothing fetched yet.</p>
{:else}
  <p class="text-muted">Loading…</p>
{/if}
