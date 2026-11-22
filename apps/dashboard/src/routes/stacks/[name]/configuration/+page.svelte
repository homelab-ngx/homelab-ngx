<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { page } from "$app/state";
  import { describe } from "#lib/api/client.ts";
  import { busy, busyPoll, fetchStack, idlePoll, keys } from "#lib/api/queries.ts";
  import StackMappings from "#lib/components/StackMappings.svelte";
  import { stackTitle } from "#lib/stacks.ts";
  import { missingVariables } from "#lib/variables.ts";

  const name = $derived(page.params.name ?? "");
  const stack = createQuery(() => ({
    queryKey: [...keys.stacks, name],
    queryFn: () => fetchStack(name),
    refetchInterval: (q) => (q.state.data && busy(q.state.data) ? busyPoll : idlePoll),
  }));
</script>

<svelte:head><title>Configuration · {stack.data ? stackTitle(stack.data) : name} · Homelab-ngx</title></svelte:head>

{#if stack.error}
  <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(stack.error)}</p>
{:else if stack.data}
  {@const s = stack.data}
  <h1 class="sr-only">{stackTitle(s)} configuration</h1>
  {#if !s.variables_ready}
    {@const missing = missingVariables(s)}
    <p role="alert" class="mb-4 rounded-md bg-warn-soft px-3 py-2 text-warn">
      {#if missing.length}Needs values before it can update: {missing.join(", ")}.{:else}The variables could not be read. See the daemon's log.{/if}
    </p>
  {/if}
  <StackMappings stack={s} />
{:else}
  <p class="text-muted">Loading…</p>
{/if}
