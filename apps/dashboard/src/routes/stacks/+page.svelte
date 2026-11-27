<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { LayoutGrid, Rows3, Search } from "@lucide/svelte";
  import { api, call, describe } from "#lib/api/client.ts";
  import { busy, busyPoll, fetchDrafts, fetchServices, fetchStacks, idlePoll, keys } from "#lib/api/queries.ts";
  import RestoreStacksDialog from "#lib/components/RestoreStacksDialog.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import StackGrid from "#lib/components/StackGrid.svelte";
  import { ago } from "#lib/format.ts";
  import { health, servicesOf, shortName } from "#lib/stacks.ts";
  import { setStacksView, stacksView } from "#lib/stacksView.svelte.ts";

  let restoring = $state(false);

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
      down > 0 ? `${down} down` : "",
    ].filter(Boolean);
  });

  /** Stacks whose update needs no approval and can go now. */
  const ready = $derived(list.filter((s) => s.generation > 0 && s.update_state === "commits_available" && s.variables_ready && !busy(s)).length);

  let query = $state("");
  /** A stack matches by its name, its label or one of its services. */
  const shown = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!q) return list;
    return list.filter((s) =>
      [s.name, s.label ?? "", ...servicesOf(services.data, s.name).map((u) => shortName(s.name, u.name))].some((t) => t.toLowerCase().includes(q)),
    );
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

  const menu = $derived([
    {
      label: ready > 0 ? `Update ${ready} stack${ready === 1 ? "" : "s"}` : "Update stacks",
      disabled: ready === 0 || updateAll.isPending || anyBusy,
      title: ready === 0 ? "No update can go without approval" : anyBusy ? "A stack is busy" : undefined,
      onSelect: (): void => {
        updateAll.mutate();
      },
    },
    {
      label: "Restore…",
      disabled: anyBusy,
      onSelect: (): void => {
        restoring = true;
      },
    },
  ]);
</script>

<svelte:head><title>Stacks · Homelab-ngx</title></svelte:head>

<div class="flex flex-wrap items-end justify-between gap-3">
  <div>
    <h1 class="text-xl font-semibold">Stacks</h1>
    <p class="mt-0.5 text-muted">{summary.join(" · ")}</p>
  </div>
  <a class="btn btn-primary" href="/stacks/add">Add stack</a>
</div>

{#if stacks.data && list.length > 0}
  <div class="mt-4 flex flex-wrap items-center gap-x-3 gap-y-2">
    <label class="relative flex min-w-0 flex-[1_1_18rem] items-center">
      <Search class="pointer-events-none absolute left-2.5 size-4 text-muted" aria-hidden="true" />
      <input type="search" class="input pl-8" placeholder="Search {list.length} stacks" aria-label="Search stacks" bind:value={query} />
    </label>
    <div role="group" aria-label="Card detail" class="inline-flex overflow-hidden rounded-md border border-line">
      <button
        type="button"
        aria-pressed={stacksView.view === "minimal"}
        onclick={() => setStacksView("minimal")}
        class="btn rounded-none border-0 focus-visible:-outline-offset-2 {stacksView.view === 'minimal' ? 'bg-accent-soft text-accent hover:bg-accent-soft' : ''}"
      >
        <LayoutGrid class="size-3.5" aria-hidden="true" /><span class="max-sm:sr-only">Minimal</span>
      </button>
      <button
        type="button"
        aria-pressed={stacksView.view === "detailed"}
        onclick={() => setStacksView("detailed")}
        class="btn rounded-none border-y-0 border-r-0 focus-visible:-outline-offset-2 {stacksView.view === 'detailed' ? 'bg-accent-soft text-accent hover:bg-accent-soft' : ''}"
      >
        <Rows3 class="size-3.5" aria-hidden="true" /><span class="max-sm:sr-only">Detailed</span>
      </button>
    </div>
    <div class="flex items-center gap-2">
      <button type="button" class="btn" disabled={check.isPending} onclick={() => check.mutate()}>
        {check.isPending ? "Checking…" : "Check for updates"}
      </button>
      <RowMenu name="Stacks" items={menu} vertical />
    </div>
  </div>
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
      <div class="mt-3 flex justify-center gap-2">
        <a class="btn btn-primary" href="/stacks/add">Add stack</a>
        <button type="button" class="btn" onclick={() => (restoring = true)}>Restore…</button>
      </div>
    </div>
  {:else if stacks.data && shown.length === 0}
    <p class="rounded-lg border border-line bg-surface px-4 py-3 text-muted">No stack matches.</p>
  {:else if stacks.data}
    <StackGrid stacks={shown} services={services.data ?? []} view={stacksView.view} />
  {:else}
    <p class="text-muted">Loading…</p>
  {/if}
  {#if restoring}<RestoreStacksDialog onClose={() => (restoring = false)} />{/if}
</div>
