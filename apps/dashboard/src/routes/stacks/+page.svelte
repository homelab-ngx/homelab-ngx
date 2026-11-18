<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe } from "#lib/api/client.ts";
  import { busy, busyPoll, fetchDrafts, fetchServices, fetchStacks, idlePoll, keys } from "#lib/api/queries.ts";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import StackTable from "#lib/components/StackTable.svelte";
  import { ago } from "#lib/format.ts";
  import { health, servicesOf } from "#lib/stacks.ts";

  const client = useQueryClient();
  const stacks = createQuery(() => ({
    queryKey: keys.stacks,
    queryFn: fetchStacks,
    refetchInterval: (q) => (q.state.data?.some(busy) ? busyPoll : idlePoll),
  }));
  const services = createQuery(() => ({ queryKey: keys.services, queryFn: fetchServices, refetchInterval: idlePoll }));
  const drafts = createQuery(() => ({ queryKey: keys.drafts, queryFn: fetchDrafts }));
  /** Stacks being written in the dashboard that are not installed yet. */
  const unfinished = $derived((drafts.data ?? []).filter((d) => d.installed_as.length === 0));

  const list = $derived(stacks.data ?? []);
  const anyBusy = $derived(list.some(busy));

  const summary = $derived.by(() => {
    const svcs = (services.data ?? []).filter((s) => s.stack);
    const down = list.reduce((n, s) => n + health(s, servicesOf(services.data, s.name)).failed.length, 0);
    return [
      `${list.length} stack${list.length === 1 ? "" : "s"}`,
      `${svcs.length} service${svcs.length === 1 ? "" : "s"}`,
      down > 0 ? `${down} service${down === 1 ? "" : "s"} down` : "",
    ].filter(Boolean);
  });

  /** The updates strip: what is waiting and what can go without approval. */
  const updates = $derived.by(() => {
    const installed = list.filter((s) => s.generation > 0);
    const waiting = installed.filter((s) => s.update_state !== "up_to_date");
    return {
      waiting: waiting.length,
      approval: waiting.filter((s) => s.update_state === "needs_approval").length,
      ready: waiting.filter((s) => s.update_state === "commits_available" && s.variables_ready && !busy(s)).length,
      checkedAt: installed.reduce<string | null>((max, s) => (!max || s.fetched.at > max ? s.fetched.at : max), null),
    };
  });

  let error = $state("");
  const refresh = () => client.invalidateQueries({ queryKey: keys.all });
  const check = createMutation(() => ({
    mutationFn: () => call(api.POST("/stacks-check")),
    onMutate: () => (error = ""),
    onError: (e: unknown) => (error = `Check for updates: ${describe(e)}`),
    onSettled: refresh,
  }));
  const updateAll = createMutation(() => ({
    mutationFn: () => call(api.POST("/stacks-update")),
    onMutate: () => (error = ""),
    onError: (e: unknown) => (error = `Update: ${describe(e)}`),
    onSettled: refresh,
  }));
</script>

<svelte:head><title>Stacks · Homelab-ngx</title></svelte:head>

<div class="flex flex-wrap items-end justify-between gap-3">
  <div>
    <h1 class="text-xl font-semibold">Stacks</h1>
    <p class="mt-0.5 text-muted">{summary.join(" · ")}</p>
  </div>
  <a class="btn btn-primary" href="/stacks/add">Add stack</a>
</div>

{#if stacks.data && list.some((s) => s.generation > 0)}
  <section aria-label="Updates" class="mt-4 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-line bg-surface px-3 py-2">
    <h2 class="label">
      <Help label="Updates">
        Update installs the new version of each stack that asks for no new access, one at a time. A stack that does not come up
        healthy returns to its previous version.
      </Help>
    </h2>
    {#if updates.waiting > 0}
      <Pill tone="info">{updates.waiting} waiting</Pill>
    {:else}
      <Pill tone="ok">current</Pill>
    {/if}
    <p class="min-w-40 flex-1 text-muted">
      {#if updates.approval > 0}{updates.approval} need{updates.approval === 1 ? "s" : ""} approval ·{/if}
      checked {ago(updates.checkedAt)}
    </p>
    <div class="flex flex-wrap items-center gap-2">
      <button type="button" class="btn" disabled={check.isPending} onclick={() => check.mutate()}>
        {check.isPending ? "Checking…" : "Check now"}
      </button>
      {#if updates.ready > 0}
        <button type="button" class="btn btn-primary" disabled={updateAll.isPending || anyBusy} onclick={() => updateAll.mutate()}>
          Update {updates.ready} stack{updates.ready === 1 ? "" : "s"}
        </button>
      {/if}
    </div>
  </section>
{/if}

{#if error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
{/if}

{#if unfinished.length}
  <section aria-labelledby="drafts-h" class="mt-4 rounded-lg border border-dashed border-line px-3 py-2">
    <h2 id="drafts-h" class="label">Being written</h2>
    <ul class="mt-1 flex flex-wrap gap-x-4 gap-y-1">
      {#each unfinished as d (d.name)}
        <li>
          <a class="link" href="/stacks/drafts/{d.name}">{d.name}</a>
          <span class="text-xs text-muted">saved {ago(d.saved_at)}</span>
        </li>
      {/each}
    </ul>
  </section>
{/if}

<div class="mt-4">
  {#if stacks.error}
    <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(stacks.error)}</p>
  {:else if stacks.data && stacks.data.length === 0}
    <div class="card text-center">
      <p>No stacks yet.</p>
      <a class="btn btn-primary mt-3" href="/stacks/add">Add stack</a>
    </div>
  {:else if stacks.data}
    <StackTable stacks={stacks.data} services={services.data ?? []} />
  {:else}
    <p class="text-muted">Loading…</p>
  {/if}
</div>
