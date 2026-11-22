<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { api, call, describe, type Stack } from "#lib/api/client.ts";
  import { busy, busyPoll, fetchDraft, fetchDrafts, fetchServices, fetchStack, fetchUpdates, idlePoll, keys } from "#lib/api/queries.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import HistoryDialog from "#lib/components/HistoryDialog.svelte";
  import LogsDialog from "#lib/components/LogsDialog.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import OperationCard from "#lib/components/OperationCard.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import ForkStack from "#lib/components/ForkStack.svelte";
  import RenameStack from "#lib/components/RenameStack.svelte";
  import StackTable from "#lib/components/StackTable.svelte";
  import ReviewDialog from "#lib/components/ReviewDialog.svelte";
  import StackNixpkgs from "#lib/components/StackNixpkgs.svelte";
  import StackMappings from "#lib/components/StackMappings.svelte";
  import { shortRev, stamp } from "#lib/format.ts";
  import { describeSource, draftOf, grants, logId, nixpkgsOnly, removedGrants, routeLinks, runningNixpkgs, servicesOf, sourceHref, stackTitle } from "#lib/stacks.ts";
  import { missingVariables } from "#lib/variables.ts";

  const name = $derived(page.params.name ?? "");
  const client = useQueryClient();

  const stack = createQuery(() => ({
    queryKey: [...keys.stacks, name],
    queryFn: () => fetchStack(name),
    refetchInterval: (q) => (q.state.data && busy(q.state.data) ? busyPoll : idlePoll),
  }));
  const services = createQuery(() => ({ queryKey: keys.services, queryFn: fetchServices, refetchInterval: idlePoll }));
  /** The draft a stack runs from, for where a fork came from. */
  const draftName = $derived(stack.data ? draftOf(stack.data.source) : null);
  const draft = createQuery(() => ({
    queryKey: keys.draft(draftName ?? ""),
    queryFn: () => fetchDraft(draftName!),
    enabled: draftName !== null,
  }));
  /** Drafts forked from this stack, for the Source box's Fork row. */
  const drafts = createQuery(() => ({ queryKey: keys.drafts, queryFn: fetchDrafts, enabled: !!stack.data }));
  const forks = $derived((drafts.data ?? []).filter((d) => stack.data && d.forked_from === stack.data.source));
  const attempts = createQuery(() => ({
    queryKey: keys.updates(name),
    queryFn: () => fetchUpdates(name, 100),
    refetchInterval: stack.data && busy(stack.data) ? busyPoll : idlePoll,
  }));

  let error = $state("");
  let removing = $state(false);
  let historyOpen = $state(false);
  let renaming = $state(false);
  let forking = $state(false);
  let reviewing = $state<Stack | null>(null);
  // A link to a service's logs (?logs=<unit>, from an old /logs link) opens them.
  let logsUnit = $state<string | null>(page.url.searchParams.get("logs"));

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
  const rollback = createMutation(() => ({
    mutationFn: (generation: number) => call(api.POST("/stacks/{name}/rollback", { ...path(), body: { generation } })),
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
    start.isPending ||
      stop.isPending ||
      update.isPending ||
      rollback.isPending ||
      freeze.isPending ||
      thaw.isPending ||
      (stack.data ? busy(stack.data) : false),
  );

  /** A grant list in a few words: "4 routes, 1 folder". */
  function accessSummary(list: { kind: string }[]): string {
    const words: Record<string, [string, string]> = { route: ["route", "routes"], storage: ["folder", "folders"], port: ["port", "ports"], egress: ["egress rule", "egress rules"] };
    const counts: Record<string, number> = {};
    for (const g of list) counts[g.kind] = (counts[g.kind] ?? 0) + 1;
    const parts = Object.entries(counts).map(([k, n]) => (words[k] ? `${n} ${words[k][n === 1 ? 0 : 1]}` : k));
    return parts.join(", ") || "none";
  }

  /** The host directories a removal leaves alone: fixed storage, the paths' values and the added folders. */
  function keptPaths(s: Stack): string[] {
    const fixed = (s.approved?.manifest.storage ?? []).map((st) => st.host_path);
    const chosen = s.variables.filter((v) => v.kind === "path" && v.value).map((v) => v.value!);
    const added = (s.folders ?? []).map((f) => f.path);
    return [...new Set([...fixed, ...chosen, ...added])];
  }

</script>

{#snippet maybeLink(href: string | null, text: string)}
  {#if href}<a class="link" {href} target="_blank" rel="noreferrer">{text}</a>{:else}{text}{/if}
{/snippet}

<svelte:head><title>{stack.data ? stackTitle(stack.data) : name} · Stacks · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/stacks">Stacks</a> /</p>

{#if stack.error}
  <p role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(stack.error)}</p>
{:else if stack.data}
  {@const s = stack.data}
  {@const src = describeSource(s.source)}
  {@const href = sourceHref(s.source)}
  {@const title = stackTitle(s)}
  {@const access = grants(s.approved?.manifest)}
  <div class="mt-1 flex flex-wrap items-center justify-between gap-3">
    <div>
      <h1 class="flex items-center gap-3 text-xl font-semibold">
        {title}
        {#if s.frozen}<Pill tone="info">Frozen</Pill>{:else if s.running}<Pill tone="ok">Running</Pill>{:else}<Pill tone="idle">Stopped</Pill>{/if}
      </h1>
      {#if s.label}
        <p class="text-muted">{s.name}</p>
      {/if}
      {#if routeLinks(s.approved?.manifest).length > 0}
        <p class="mt-1 flex flex-wrap gap-x-3">
          {#each routeLinks(s.approved?.manifest) as l (l)}
            <a class="link" href={l} target="_blank" rel="noreferrer">{l.slice(8)}</a>
          {/each}
        </p>
      {/if}
      <label class="mt-1 flex items-center gap-2 text-muted">
        <input type="checkbox" class="accent-accent" checked={s.autostart} disabled={autostart.isPending} onchange={(e) => autostart.mutate(e.currentTarget.checked)} />
        Start at boot
      </label>
    </div>
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn" onclick={() => (renaming = true)}>Rename…</button>
      <button type="button" class="btn" onclick={() => (historyOpen = true)}>History</button>
      {#if draftOf(s.source)}
        <a class="btn" href="/stacks/drafts/{draftOf(s.source)}">Edit</a>
      {:else}
        <button type="button" class="btn" disabled={!s.approved} onclick={() => (forking = true)}>Fork…</button>
      {/if}
      <button type="button" class="btn" disabled={own.length === 0} onclick={() => (logsUnit = logId(own[0]!.name))}>Logs</button>
      {#if s.frozen}
        <button type="button" class="btn btn-primary" disabled={working} onclick={() => thaw.mutate()}>Thaw</button>
        <button type="button" class="btn" disabled={working} onclick={() => stop.mutate()}>Stop</button>
      {:else if s.running}
        <button type="button" class="btn" disabled={working} onclick={() => freeze.mutate()}>Freeze</button>
        <button type="button" class="btn" disabled={working} onclick={restart}>Restart</button>
        <button type="button" class="btn" disabled={working} onclick={() => stop.mutate()}>Stop</button>
      {:else}
        <button type="button" class="btn" disabled={working || !s.approved} onclick={() => start.mutate()}>Start</button>
      {/if}
      <button type="button" class="btn btn-danger" disabled={working} onclick={() => (removing = true)}>Remove…</button>
    </div>
  </div>

  {#if s.frozen}
    <p role="status" class="mt-4 rounded-md bg-accent-soft px-3 py-2 text-accent">Frozen. Its addresses don't answer, and a reboot loses its memory.</p>
  {/if}

  {#if s.update_state !== "up_to_date" && s.generation > 0 && !busy(s)}
    {@const fresh = s.new_grants.length}
    {@const dropped = removedGrants(s).length}
    <section aria-label="Update" class="mt-4 rounded-lg border border-accent/40 bg-accent-soft px-3 py-2.5">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
        <p class="min-w-48 flex-1">
          <span class="font-semibold">Update ready</span>
          {#if s.update_state === "commits_available" && nixpkgsOnly(s)}
            <span class="text-muted">{shortRev(runningNixpkgs(s))} → {shortRev(s.nixpkgs_rev)}</span>
            <span class="text-muted">· new NixOS packages</span>
          {:else}
          <span class="text-muted">{shortRev(s.approved?.rev)} → {shortRev(s.fetched.rev)}</span>
          <span class="text-muted">
            · {s.commits.length || "new"} commit{s.commits.length === 1 ? "" : "s"} ·
            {s.update_state === "needs_approval"
              ? "asks for new access"
              : dropped > 0
                ? "less access"
                : "no new access"}
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
    </section>
  {/if}

  {#if error}
    <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
  {/if}

  {#if !s.variables_ready}
    {@const missing = missingVariables(s)}
    <p role="alert" class="mt-4 rounded-md bg-warn-soft px-3 py-2 text-warn">
      {#if missing.length}
        Needs values before it can update: {missing.join(", ")}.
      {:else}
        The variables could not be read. See the daemon's log.
      {/if}
    </p>
  {/if}

  {#if s.restart_needed}
    <div role="status" class="mt-4 flex flex-wrap items-center justify-between gap-2 rounded-md bg-warn-soft px-3 py-2 text-warn">
      <p>Restart to apply changed values, folders or API access.</p>
      <button type="button" class="btn btn-sm" disabled={working} onclick={restart}>Restart</button>
    </div>
  {/if}

  {#if s.operation && (busy(s) || s.operation.error)}
    <div class="mt-4"><OperationCard stack={s} attempt={attempts.data?.[0]} /></div>
  {/if}

  <section class="mt-5" aria-labelledby="services-h">
    <h2 id="services-h" class="mb-2 font-semibold">Services</h2>
    <StackTable stacks={[s]} services={services.data ?? []} expandAll />
  </section>

  <div class="mt-5"><StackMappings stack={s} /></div>

  <!-- Looked at rarely: closed until opened, and an opened one stays open on this device. -->
  <section class="card mt-4" aria-label="Source">
    <Disclosure section label="Source" summary={src.repo} remember="stack-source">
      <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5">
        <dt class="text-muted">{src.local ? "Folder" : "Repo"}</dt>
        <dd class="break-all">{@render maybeLink(href.repo, src.repo)}</dd>
        <dt class="text-muted">Branch</dt>
        <dd class="break-all">{@render maybeLink(href.branch, src.branch)}</dd>
        <dt class="text-muted">Pinned</dt>
        <dd>{s.approved ? `${shortRev(s.approved.rev)} · ${stamp(s.approved.at)}` : "nothing approved"}</dd>
        {#if draft.data?.forked_from}
          <dt class="text-muted">Forked from</dt>
          <dd class="break-all">
            {@render maybeLink(sourceHref(draft.data.forked_from).repo, describeSource(draft.data.forked_from).repo)} · {shortRev(draft.data.forked_rev ?? "")}
          </dd>
        {/if}
        <dt class="text-muted">Fork</dt>
        <dd>
          {#if forks.length}
            <ul>
              {#each forks as f (f.name)}<li><a class="link" href="/stacks/drafts/{f.name}">{f.name}</a></li>{/each}
            </ul>
          {:else}
            <button type="button" class="link" disabled={!s.approved} onclick={() => (forking = true)}>Fork…</button>
          {/if}
        </dd>
      </dl>
      {#if s.source_dirty}
        <p class="mt-3 text-warn">Uncommitted changes in the folder are not installed.</p>
      {/if}
    </Disclosure>
  </section>

  <section class="card mt-4" aria-label="Approved grants">
    <Disclosure section label="Approved grants" summary={accessSummary(access)} remember="stack-access">
      {#snippet help()}
        What this stack may reach outside its own container: host folders, web addresses, ports, the GPU. Each was approved here.
        Anything not listed is blocked, including the LAN and Tailscale.
      {/snippet}
      <ul class="divide-y divide-line">
        {#each access as g (g.kind + g.detail)}
          <li class="flex gap-3 py-1.5">
            <span class="label w-20 shrink-0 pt-0.5">{g.kind}</span>
            <span class="break-all">{g.detail}</span>
          </li>
        {/each}
      </ul>
    </Disclosure>
  </section>

  <div class="mt-4"><StackNixpkgs stack={s} /></div>

  <Modal
    open={removing}
    onOpenChange={(o) => (removing = o)}
    title="Remove {title}?"
    description="Data stays on this host."
  >
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

  <HistoryDialog
    stack={s}
    attempts={attempts.data ?? []}
    open={historyOpen}
    onClose={() => (historyOpen = false)}
    {working}
    onRollback={(g) => rollback.mutate(g)}
  />
  <LogsDialog stack={s.name} {title} services={own} unit={logsUnit} onClose={() => (logsUnit = null)} />
  <ReviewDialog stack={reviewing} onClose={() => (reviewing = null)} />
  {#if renaming}<RenameStack stack={s} onClose={() => (renaming = false)} />{/if}
  {#if forking}<ForkStack stack={s} onClose={() => (forking = false)} />{/if}
{:else}
  <p class="mt-3 text-muted">Loading…</p>
{/if}

