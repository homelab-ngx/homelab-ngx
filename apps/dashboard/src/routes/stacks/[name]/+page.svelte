<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { ExternalLink, Play, RotateCcw, Snowflake, Square, SquareTerminal } from "@lucide/svelte";
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { api, call, describe, type Stack } from "#lib/api/client.ts";
  import { busy, busyPoll, fetchServices, fetchStack, fetchUpdates, idlePoll, keys } from "#lib/api/queries.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import OperationCard from "#lib/components/OperationCard.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import RenameStack from "#lib/components/RenameStack.svelte";
  import ReviewDialog from "#lib/components/ReviewDialog.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import ServicesTable from "#lib/components/ServicesTable.svelte";
  import StackIcon from "#lib/components/StackIcon.svelte";
  import Switch from "#lib/components/Switch.svelte";
  import UpdateChip from "#lib/components/UpdateChip.svelte";
  import { bytes, cpu, shortRev, stamp } from "#lib/format.ts";
  import {
    describeSource,
    health,
    nixpkgsLabel,
    nixpkgsOnly,
    removedGrants,
    routeLinks,
    runningNixpkgs,
    servicesOf,
    sourceHref,
    stackIcon,
    stackTitle,
    stateLabel,
    stateTone,
  } from "#lib/stacks.ts";
  import { openShellWindow } from "#lib/shell.ts";
  import { missingVariables } from "#lib/variables.ts";

  const name = $derived(page.params.name ?? "");
  const client = useQueryClient();

  const stack = createQuery(() => ({
    queryKey: [...keys.stacks, name],
    queryFn: () => fetchStack(name),
    refetchInterval: (q) => (q.state.data && busy(q.state.data) ? busyPoll : idlePoll),
  }));
  const services = createQuery(() => ({ queryKey: keys.services, queryFn: fetchServices, refetchInterval: idlePoll }));
  /** The latest attempt, for the operation card's package changes; the History tab reads the same list. */
  const attempts = createQuery(() => ({
    queryKey: keys.updates(name),
    queryFn: () => fetchUpdates(name, 100),
    refetchInterval: stack.data && busy(stack.data) ? busyPoll : idlePoll,
  }));

  // An old link to a service's logs (?logs=<unit>) goes to the Logs tab.
  onMount(() => {
    const unit = page.url.searchParams.get("logs");
    if (unit) goto(`/stacks/${name}/logs?unit=${encodeURIComponent(unit)}`, { replaceState: true });
  });

  let error = $state("");
  let removing = $state(false);
  let renaming = $state(false);
  let reviewing = $state<Stack | null>(null);

  const own = $derived(servicesOf(services.data, name));

  const refresh = () => client.invalidateQueries({ queryKey: keys.all });
  const op = (fn: () => Promise<unknown>) =>
    createMutation(() => ({
      mutationFn: fn,
      onMutate: () => (error = ""),
      onError: (e: unknown) => (error = describe(e)),
      onSettled: refresh,
    }));
  const path = () => ({ params: { path: { name } } });
  const start = op(() => call(api.POST("/stacks/{name}/start", path())));
  const stop = op(() => call(api.POST("/stacks/{name}/stop", path())));
  const update = op(() => call(api.POST("/stacks/{name}/update", path())));
  const freeze = op(() => call(api.POST("/stacks/{name}/freeze", path())));
  const thaw = op(() => call(api.POST("/stacks/{name}/thaw", path())));
  const autostart = createMutation(() => ({
    mutationFn: (on: boolean) => call(api.PATCH("/stacks/{name}", { ...path(), body: { autostart: on } })),
    onMutate: () => (error = ""),
    onError: (e: unknown) => (error = describe(e)),
    onSettled: refresh,
  }));
  const remove = createMutation(() => ({
    mutationFn: () => call(api.DELETE("/stacks/{name}", path())),
    onError: (e: unknown) => (error = describe(e)),
    onSuccess: async () => {
      removing = false;
      // Leave first: this page's own queries would ask for the stack that
      // is gone.
      await goto("/stacks");
      client.removeQueries({ queryKey: [...keys.stacks, name] });
      client.removeQueries({ queryKey: keys.updates(name) });
      await refresh();
    },
  }));

  /** Restart is a stop, then a start once the stop has finished. */
  async function restart() {
    error = "";
    try {
      await call(api.POST("/stacks/{name}/stop", path()));
      for (;;) {
        await new Promise((r) => setTimeout(r, 1500));
        const s = await fetchStack(name);
        if (!busy(s)) {
          if (s.operation?.error) throw new Error(s.operation.error);
          break;
        }
      }
      await call(api.POST("/stacks/{name}/start", path()));
    } catch (e) {
      error = describe(e);
    }
    await refresh();
  }

  const working = $derived(
    start.isPending || stop.isPending || update.isPending || freeze.isPending || thaw.isPending || (stack.data ? busy(stack.data) : false),
  );

  /** The Controls card's menu: the rarer actions, Remove last. A fork starts in the Source code tab. */
  function menu() {
    return [
      { label: "Rename…", onSelect: () => (renaming = true) },
      { label: "Remove…", danger: true, disabled: working, onSelect: () => (removing = true) },
    ];
  }

  /** The host directories a removal leaves alone: fixed storage, the paths' values and the added folders. */
  function keptPaths(s: Stack): string[] {
    const fixed = (s.approved?.manifest.storage ?? []).map((st) => st.host_path);
    const chosen = s.variables.filter((v) => v.kind === "path" && v.value).map((v) => v.value!);
    const added = (s.folders ?? []).map((f) => f.path);
    return [...new Set([...fixed, ...chosen, ...added])];
  }
</script>

<svelte:head><title>{stack.data ? stackTitle(stack.data) : name} · Stacks · Homelab-ngx</title></svelte:head>

{#if stack.error}
  <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(stack.error)}</p>
{:else if stack.data}
  {@const s = stack.data}
  {@const src = describeSource(s.source)}
  {@const href = sourceHref(s.source)}
  {@const title = stackTitle(s)}
  {@const h = health(s, own)}
  {@const gen = s.generations.find((g) => g.id === s.generation)}
  <div class="flex flex-wrap items-start gap-4">
    <div class="flex min-w-0 flex-[999_1_32rem] flex-col gap-4">
      <section aria-labelledby="stack-h" class="overflow-hidden rounded-lg border border-line bg-surface">
        <div class="flex flex-col gap-3.5 p-5">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <StackIcon src={stackIcon(s)} name={title} size="lg" />
            <div class="flex flex-wrap items-center gap-3">
              <UpdateChip stack={s} />
              <Pill tone={stateTone[h.state]}>
                {stateLabel[h.state]}{#if s.running && h.active < h.total}&nbsp;<span class="text-muted">{h.active}/{h.total}</span>{/if}
              </Pill>
            </div>
          </div>
          <div>
            <h1 id="stack-h" class="text-xl font-semibold">{title}</h1>
            <p class="mt-0.5 text-muted">
              {#if s.label}{s.name} ·{/if}
              {#if gen}version {shortRev(gen.rev)} · built {stamp(gen.built_at)} ·{/if}
              {#if href.repo}<a class="link" href={href.repo} target="_blank" rel="noreferrer">{src.repo}</a>{:else}{src.repo}{/if}
            </p>
          </div>
          {#if routeLinks(s.approved?.manifest).length > 0}
            <ul class="flex flex-wrap gap-1.5" aria-label="Addresses">
              {#each routeLinks(s.approved?.manifest) as l (l)}
                <li>
                  <a
                    href={l}
                    target="_blank"
                    rel="noreferrer"
                    class="inline-flex items-center gap-1.5 rounded-sm border border-line bg-sunken px-1.5 text-xs text-fg hover:border-accent hover:text-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                    >{l.slice(8)}<ExternalLink class="size-3 text-muted" aria-hidden="true" /></a
                  >
                </li>
              {/each}
            </ul>
          {/if}
        </div>
        {#if s.update_state !== "up_to_date" && s.generation > 0 && !busy(s)}
          {@const dropped = removedGrants(s).length}
          <div class="border-t border-accent/40 bg-accent-soft px-5 py-2.5">
            <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
              <p class="min-w-48 flex-1">
                <span class="font-semibold">Update ready</span>
                {#if s.update_state === "commits_available" && nixpkgsOnly(s)}
                  <span class="text-muted">{shortRev(runningNixpkgs(s))} → {shortRev(s.nixpkgs_rev)} · new NixOS packages</span>
                {:else}
                  <span class="text-muted">
                    {shortRev(s.approved?.rev)} → {shortRev(s.fetched.rev)} · {s.commits.length || "new"} commit{s.commits.length === 1 ? "" : "s"} ·
                    {s.update_state === "needs_approval" ? "asks for new access" : dropped > 0 ? "less access" : "no new access"}
                  </span>
                {/if}
              </p>
              {#if s.update_state === "needs_approval"}
                <button type="button" class="btn btn-primary" onclick={() => (reviewing = s)}>Review…</button>
              {:else}
                <button type="button" class="btn btn-primary" disabled={working || !s.variables_ready} onclick={() => update.mutate()}>Update</button>
              {/if}
            </div>
            {#if s.commits.length > 0 && s.update_state === "commits_available"}
              <Disclosure label="What changed" class="mt-1.5">
                <ul class="text-xs">
                  {#each s.commits as c (c.rev)}
                    <li class="truncate"><span class="text-muted">{shortRev(c.rev)}</span> {c.subject}</li>
                  {/each}
                </ul>
              </Disclosure>
            {/if}
          </div>
        {/if}
      </section>

      {#if s.frozen}
        <p role="status" class="rounded-md bg-accent-soft px-3 py-2 text-accent">Frozen. Its addresses don't answer, and a reboot loses its memory.</p>
      {/if}
      {#if error}
        <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
      {/if}
      {#if !s.variables_ready}
        {@const missing = missingVariables(s)}
        <p role="alert" class="rounded-md bg-warn-soft px-3 py-2 text-warn">
          {#if missing.length}
            Needs values before it can update: {missing.join(", ")}. <a class="underline" href="/stacks/{name}/configuration">Configuration</a>
          {:else}
            The variables could not be read. See the daemon's log.
          {/if}
        </p>
      {/if}
      {#if s.restart_needed}
        <div role="status" class="flex flex-wrap items-center justify-between gap-2 rounded-md bg-warn-soft px-3 py-2 text-warn">
          <p>Restart to apply changed values, folders or API access.</p>
          <button type="button" class="btn btn-sm" disabled={working} onclick={restart}>Restart</button>
        </div>
      {/if}
      {#if s.idle_grants.length}
        <div role="status" class="grid gap-1 rounded-md bg-warn-soft px-3 py-2 text-warn">
          <p>Open nothing now:</p>
          <ul class="grid gap-1">
            {#each s.idle_grants as g (g.detail)}
              <li>{g.detail}. {g.reason}.</li>
            {/each}
          </ul>
          {#if s.idle_grants.some((g) => g.reason.includes("LAN subnet"))}
            <p><a class="underline" href="/settings/network">Network settings</a></p>
          {/if}
        </div>
      {/if}
      {#if s.operation && (busy(s) || s.operation.error)}
        <OperationCard stack={s} attempt={attempts.data?.[0]} />
      {/if}

      <section aria-labelledby="services-h" class="overflow-hidden rounded-lg border border-line bg-surface">
        <div class="flex items-baseline gap-2 px-4 py-3">
          <h2 id="services-h" class="font-semibold">Services</h2>
          {#if h.total > 0}<span class="text-muted">{h.active} of {h.total} active</span>{/if}
        </div>
        <ServicesTable stack={s} services={own} />
      </section>
    </div>

    <aside aria-labelledby="controls-h" class="card flex min-w-0 flex-[1_1_18rem] flex-col gap-4">
      <div class="flex items-center justify-between gap-2">
        <h2 id="controls-h" class="font-semibold">Controls</h2>
        <RowMenu name={title} items={menu()} vertical />
      </div>
      <div class="flex flex-wrap gap-2">
        {#if s.frozen}
          <button type="button" class="btn btn-primary" disabled={working} onclick={() => thaw.mutate()}><Play class="size-3.5" aria-hidden="true" />Thaw</button>
          <button type="button" class="btn" disabled={working} onclick={() => stop.mutate()}><Square class="size-3.5" aria-hidden="true" />Stop</button>
        {:else if s.running}
          <button type="button" class="btn" disabled={working} onclick={restart}><RotateCcw class="size-3.5" aria-hidden="true" />Restart</button>
          <button type="button" class="btn" disabled={working} onclick={() => stop.mutate()}><Square class="size-3.5" aria-hidden="true" />Stop</button>
          <button type="button" class="btn" disabled={working} onclick={() => freeze.mutate()}><Snowflake class="size-3.5" aria-hidden="true" />Freeze</button>
          <button type="button" class="btn" onclick={() => openShellWindow(name)}><SquareTerminal class="size-3.5" aria-hidden="true" />Shell</button>
        {:else}
          <button type="button" class="btn btn-primary" disabled={working || !s.approved} onclick={() => start.mutate()}><Play class="size-3.5" aria-hidden="true" />Start</button>
        {/if}
      </div>

      <div class="-mx-4 flex flex-col gap-3 border-y border-line px-4 py-4">
        <div class="flex items-center justify-between gap-3">
          <span>Start at boot</span>
          <Switch checked={s.autostart} label="Start at boot" disabled={autostart.isPending} onChange={(on) => autostart.mutate(on)} />
        </div>
        <div class="flex items-center justify-between gap-3">
          <span class="text-muted">Update automatically</span>
          <Switch checked={false} label="Update automatically" disabled title="Not available yet" />
        </div>
      </div>

      <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 tabular-nums">
        <dt class="text-muted">CPU</dt>
        <dd>{s.running ? cpu(s.usage?.cpu_percent) : "–"}</dd>
        <dt class="text-muted">Memory</dt>
        <dd>{s.running ? bytes(s.usage?.memory_bytes) : "–"}</dd>
        {#if s.address}
          <dt class="text-muted">Address</dt>
          <dd class="break-all">{s.address}</dd>
        {/if}
        <dt class="text-muted">Branch</dt>
        <dd class="break-words">{src.branch}{#if s.approved} · pinned {shortRev(s.approved.rev)}{/if}</dd>
        {#if s.nixpkgs}
          <dt class="text-muted">NixOS</dt>
          <dd>{nixpkgsLabel(s.nixpkgs)}</dd>
        {/if}
      </dl>
    </aside>
  </div>

  <Modal open={removing} onOpenChange={(o) => (removing = o)} title="Remove {title}?" description="Data stays on this host.">
    <div class="space-y-3">
      <div>
        <p class="label">Deleted</p>
        <p>Container, web addresses, firewall rules, saved versions and history.</p>
      </div>
      <div>
        <p class="label">Kept</p>
        <p>Data folder, variables and host folders.</p>
        <ul class="mt-1">{#each keptPaths(s) as p (p)}<li class="break-all">{p}</li>{/each}</ul>
      </div>
      {#if s.running}<p class="text-warn">Stop the stack first.</p>{/if}
    </div>
    {#snippet actions()}
      <button type="button" class="btn btn-danger" disabled={s.running || remove.isPending} onclick={() => remove.mutate()}>Remove {title}</button>
    {/snippet}
  </Modal>

  <ReviewDialog stack={reviewing} onClose={() => (reviewing = null)} />
  {#if renaming}<RenameStack stack={s} onClose={() => (renaming = false)} />{/if}
{:else}
  <p class="text-muted">Loading…</p>
{/if}
