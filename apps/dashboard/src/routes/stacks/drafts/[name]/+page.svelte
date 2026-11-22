<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { describe } from "#lib/api/client.ts";
  import { busyPoll, fetchDraft, keys } from "#lib/api/queries.ts";
  import DraftEditor from "#lib/components/drafts/DraftEditor.svelte";
  import { building } from "#lib/drafts.ts";

  const name = $derived(page.params.name ?? "");
  const draft = createQuery(() => ({
    queryKey: keys.draft(name),
    queryFn: () => fetchDraft(name),
    refetchInterval: (q) => (building(q.state.data) ? busyPoll : false),
  }));
  /** An installed draft is edited on its stack's page, in the Source code tab. */
  const installed = $derived(draft.data?.installed_as[0]);
  $effect(() => {
    if (installed) void goto(`/stacks/${installed}/source`, { replaceState: true });
  });
</script>

<svelte:head><title>{name} · Write a stack · Homelab-ngx</title></svelte:head>

{#if draft.error}
  <p class="text-muted"><a class="link" href="/stacks">Stacks</a> / Write a stack</p>
  <h1 class="mt-1 text-xl font-semibold">{name}</h1>
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(draft.error)}</p>
{:else if draft.data && !installed}
  <!-- The editor keeps its own unsaved copy of the files, made once per draft. -->
  {#key name}
    <DraftEditor draft={draft.data} />
  {/key}
{:else}
  <p class="mt-4 text-muted">Loading…</p>
{/if}
