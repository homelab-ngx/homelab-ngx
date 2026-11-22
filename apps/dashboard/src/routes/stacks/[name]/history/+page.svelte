<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { page } from "$app/state";
  import { api, call, describe } from "#lib/api/client.ts";
  import { busy, busyPoll, fetchStack, fetchUpdates, idlePoll, keys } from "#lib/api/queries.ts";
  import HistoryTable from "#lib/components/HistoryTable.svelte";
  import { stackTitle } from "#lib/stacks.ts";

  const name = $derived(page.params.name ?? "");
  const client = useQueryClient();
  const stack = createQuery(() => ({
    queryKey: [...keys.stacks, name],
    queryFn: () => fetchStack(name),
    refetchInterval: (q) => (q.state.data && busy(q.state.data) ? busyPoll : idlePoll),
  }));
  const attempts = createQuery(() => ({
    queryKey: keys.updates(name),
    queryFn: () => fetchUpdates(name, 100),
    refetchInterval: stack.data && busy(stack.data) ? busyPoll : idlePoll,
  }));

  let error = $state("");
  const rollback = createMutation(() => ({
    mutationFn: (generation: number) => call(api.POST("/stacks/{name}/rollback", { params: { path: { name } }, body: { generation } })),
    onMutate: () => (error = ""),
    onError: (e: unknown) => (error = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: keys.all }),
  }));
</script>

<svelte:head><title>History · {stack.data ? stackTitle(stack.data) : name} · Homelab-ngx</title></svelte:head>

{#if stack.error}
  <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(stack.error)}</p>
{:else if stack.data}
  {@const s = stack.data}
  <h1 class="sr-only">{stackTitle(s)} history</h1>
  {#if error}<p role="alert" class="mb-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
  <HistoryTable stack={s} attempts={attempts.data ?? []} working={rollback.isPending || busy(s)} onRollback={(g) => rollback.mutate(g)} />
{:else}
  <p class="text-muted">Loading…</p>
{/if}
