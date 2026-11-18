<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import BackupForm from "#lib/components/backups/BackupForm.svelte";
  import DestinationForm from "#lib/components/backups/DestinationForm.svelte";
  import PasswordDialog from "#lib/components/backups/PasswordDialog.svelte";
  import LiveProgress from "#lib/components/backups/LiveProgress.svelte";
  import RunStrip from "#lib/components/backups/RunStrip.svelte";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill, { type Tone } from "#lib/components/Pill.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import { ago, bytes, duration, stamp } from "#lib/format.ts";
  import { fetchStacks, keys } from "#lib/api/queries.ts";
  import { stackTitles } from "#lib/stacks.ts";
  import {
    backupKeys,
    backupState,
    dayMarks,
    describeKeep,
    describeSchedule,
    destinationWhere,
    KIND_LABEL,
    uncovered,
    type Backup,
    type Coverage,
    type Destination,
  } from "#lib/backups.ts";

  const client = useQueryClient();
  const stacks = createQuery(() => ({ queryKey: keys.stacks, queryFn: fetchStacks }));
  const title = $derived(stackTitles(stacks.data));
  const backups = createQuery(() => ({
    queryKey: backupKeys.list,
    queryFn: () => call(api.GET("/backups")) as Promise<Backup[]>,
    refetchInterval: (q) => (q.state.data?.some((b) => b.running) ? 3_000 : 30_000),
  }));
  const destinations = createQuery(() => ({
    queryKey: backupKeys.destinations,
    queryFn: () => call<Destination[]>(api.GET("/backup-destinations")),
    refetchInterval: 60_000,
  }));
  const coverage = createQuery(() => ({
    queryKey: backupKeys.coverage,
    queryFn: () => call<Coverage>(api.GET("/backup-coverage")),
    refetchInterval: 60_000,
  }));

  const list = $derived(backups.data ?? []);
  const dests = $derived(destinations.data ?? []);
  const missing = $derived(uncovered(coverage.data));
  const summary = $derived.by(() => {
    if (backups.isPending) return "Loading…";
    if (list.length === 0) return "No backups";
    const bad = list.filter((b) => ["bad", "warn"].includes(backupState(b).tone)).length;
    const n = `${list.length} backup${list.length === 1 ? "" : "s"}`;
    return bad > 0 ? `${n} · ${bad} with problems` : n;
  });

  let error = $state("");
  /** Open dialogs. */
  let form = $state<{ backup: Backup | null; preselect: { stack: string; key: string }[] } | null>(null);
  let destForm = $state<{ destination: Destination | null } | null>(null);
  let password = $state<{ destination: Destination; generated: string } | null>(null);
  let confirm = $state<{ title: string; body: string; label: string; run: () => Promise<unknown> } | null>(null);
  let tests = $state<Record<number, Schema<"DestinationTest"> | "running">>({});
  /** Where the not-backed-up folders are added: a backup's id, or "new". */
  let target = $state<number | "new">("new");
  const targetBackup = $derived(list.find((b) => b.id === target) ?? null);

  const refresh = () => client.invalidateQueries({ queryKey: backupKeys.all });

  /** Runs one change; every change reads the page again. */
  const op = createMutation(() => ({
    mutationFn: (run: () => Promise<unknown>) => run(),
    onMutate: () => (error = ""),
    onSuccess: () => (confirm = null),
    onError: (e: unknown) => (error = describe(e)),
    onSettled: refresh,
  }));

  const at = (id: number) => ({ params: { path: { id } } });
  /** The row shows the run as it goes; the backup's own page shows it folder by folder. */
  const runNow = (b: Backup) => op.mutate(() => call(api.POST("/backups/{id}/run", at(b.id))));
  const runHref = (b: Backup) => (run: number) => `/backups/${b.id}?run=${run}`;
  const setEnabled = (b: Backup, enabled: boolean) => op.mutate(() => call(api.PATCH("/backups/{id}", { ...at(b.id), body: { enabled } })));
  function askDeleteBackup(b: Backup) {
    form = null;
    confirm = {
      title: `Delete ${b.name}?`,
      body: "The backup stops running. Files already copied stay at its destinations.",
      label: "Delete backup",
      run: () => call(api.DELETE("/backups/{id}", at(b.id))),
    };
  }
  const askDeleteDestination = (d: Destination) =>
    (confirm = {
      title: `Remove ${d.name}?`,
      body: "Its stored keys are removed. Files already copied there stay.",
      label: "Remove destination",
      run: () => call(api.DELETE("/backup-destinations/{id}", at(d.id))),
    });

  const duplicate = (d: Destination) => op.mutate(() => call(api.POST("/backup-destinations/{id}/duplicate", at(d.id))));

  async function test(d: Destination) {
    tests[d.id] = "running";
    try {
      tests[d.id] = await call(api.POST("/backup-destinations/{id}/test", at(d.id)));
    } catch (e) {
      tests[d.id] = { ok: false, detail: describe(e) };
    }
  }

  /** A destination's row state: the last test's answer, else whether it is ready for runs. */
  function destinationState(d: Destination, t: Schema<"DestinationTest"> | "running" | undefined): { tone: Tone; label: string } {
    if (t === "running") return { tone: "info", label: "Testing" };
    if (t) return t.ok ? { tone: "ok", label: "Tested" } : { tone: "bad", label: "Failed" };
    if (d.kind === "restic" && !d.password_confirmed) return { tone: "warn", label: "Skipped" };
    return d.used_by.length > 0 ? { tone: "ok", label: "Ready" } : { tone: "idle", label: "Unused" };
  }

  function destinationSaved(created: Schema<"DestinationCreated">) {
    destForm = null;
    void refresh();
    if (created.generated_password) password = { destination: created.destination, generated: created.generated_password };
  }

  const goesTo = (b: Backup) => b.destinations.map((d) => d.name).join(", ");
  /** A backup's setup problems, as short values on its row's second line. */
  function setupProblems(b: Backup): string[] {
    const lost = b.sources.filter((s) => s.problem).length;
    return [
      b.destinations.length === 0 ? "no destination" : "",
      b.destinations.some((d) => !d.ready) ? "password not saved" : "",
      lost > 0 ? `${lost} folder${lost === 1 ? "" : "s"} not found` : "",
    ].filter(Boolean);
  }
</script>

<svelte:head><title>Backups · Homelab-ngx</title></svelte:head>

<div class="flex flex-wrap items-end justify-between gap-3">
  <div>
    <h1 class="text-xl font-semibold">Backups</h1>
    <p class="mt-0.5 text-muted">{summary}</p>
  </div>
  <div class="flex flex-wrap gap-2">
    <button
      type="button"
      class="btn btn-primary"
      disabled={dests.length === 0}
      title={dests.length === 0 ? "Add a destination first" : ""}
      onclick={() => (form = { backup: null, preselect: [] })}
    >
      New backup
    </button>
  </div>
</div>

{#if error && !confirm}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
{/if}
{#each [backups.error, destinations.error, coverage.error] as e, i (i)}
  {#if e}<p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(e)}</p>{/if}
{/each}

{#if missing.length > 0}
  <section class="mt-4 rounded-lg border border-line bg-surface px-3 py-2.5" aria-label="Not backed up">
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
      <Pill tone="warn">Not backed up</Pill>
      <p class="min-w-40 flex-1">
        <Help label="{missing.length} recommended folder{missing.length === 1 ? '' : 's'}">
          Each stack lists the folders worth keeping, such as database copies or photos. None is copied until it is added to a backup, so a large folder
          never uploads unasked.
        </Help>
      </p>
      <div class="flex flex-wrap items-center gap-2">
        {#if list.length > 0}
          <label class="sr-only" for="not-backed-up-target">Add them to</label>
          <select id="not-backed-up-target" class="input w-auto" bind:value={target}>
            {#each list as b (b.id)}<option value={b.id}>{b.name}</option>{/each}
            <option value="new">A new backup</option>
          </select>
        {/if}
        <button
          type="button"
          class="btn"
          disabled={dests.length === 0}
          title={dests.length === 0 ? "Add a destination first" : ""}
          onclick={() => (form = { backup: targetBackup, preselect: missing })}
        >
          Review…
        </button>
      </div>
    </div>
    <ul class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted">
      {#each missing as m (m.stack + m.key)}
        <li>{title(m.stack)}: {m.label}</li>
      {/each}
    </ul>
  </section>
{/if}

{#if backups.data && list.length === 0}
  <div class="card mt-5">
    <h2 class="font-semibold">No backups yet</h2>
    <ol class="mt-2 list-decimal space-y-1 pl-5 text-muted">
      <li>
        <span class={dests.length > 0 ? "line-through" : "text-fg"}>Add a destination</span>
        {#if dests.length > 0}<span class="text-ok"> Done</span>{/if}
      </li>
      <li><span class={dests.length > 0 ? "text-fg" : ""}>Create a backup</span></li>
    </ol>
    <div class="mt-3">
      {#if dests.length === 0}
        <button type="button" class="btn btn-primary" onclick={() => (destForm = { destination: null })}>Add destination</button>
      {:else}
        <button type="button" class="btn btn-primary" onclick={() => (form = { backup: null, preselect: missing })}>New backup</button>
      {/if}
    </div>
  </div>
{:else}
  <div class="relative mt-5 overflow-x-auto rounded-lg border border-line bg-surface">
    <table class="w-full border-collapse text-left md:min-w-[760px]">
      <thead>
        <tr class="border-b border-line bg-sunken">
          <th scope="col" class="label px-3 py-2">Backup</th>
          <th scope="col" class="label px-3 py-2 max-sm:hidden">
            <Help label="Last 14 days">
              One mark per day. A full green bar is a good run, a shorter amber bar a partly failed one, a short red bar a failed one, an empty one a day
              without a run. A mark opens that day's run.
            </Help>
          </th>
          <th scope="col" class="label px-3 py-2 max-sm:hidden">Last run</th>
          <th scope="col" class="label px-3 py-2 max-md:hidden">Next run</th>
          <th scope="col" class="px-3 py-2"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        {#each list as b (b.id)}
          {@const st = backupState(b)}
          {@const last = b.last_run}
          {@const days = dayMarks(b.recent_runs)}
          <tr class="border-b border-line align-top last:border-0">
            <td class="px-3 py-2.5">
              <a class="link" href="/backups/{b.id}">{b.name}</a>
              <div class="text-xs text-muted">
                {goesTo(b)}{#each setupProblems(b) as p, i (p)}{#if i > 0 || b.destinations.length > 0}{" · "}{/if}<span class="text-warn">{p}</span>{/each}
              </div>
              <!-- On a phone the strip and the state sit under the name. -->
              <div class="mt-2 flex flex-col items-start gap-1.5 sm:hidden">
                <RunStrip {days} href={runHref(b)} name={b.name} />
                <Pill tone={st.tone}>{st.label}</Pill>
                {#if b.running && last}
                  <LiveProgress runId={last.id} />
                {:else if last}
                  <div class="text-xs text-muted tabular-nums">{ago(last.started_at)} · {duration(last.started_at, last.finished_at)} · {bytes(last.bytes)}</div>
                  {#if last.error && st.tone !== "ok" && st.tone !== "idle"}<div class="text-xs break-words text-bad">{last.error}</div>{/if}
                {/if}
              </div>
            </td>
            <td class="px-3 py-2.5 max-sm:hidden"><RunStrip {days} href={runHref(b)} name={b.name} /></td>
            <td class="px-3 py-2.5 max-sm:hidden">
              <Pill tone={st.tone}>{st.label}</Pill>
              {#if b.running && last}
                <LiveProgress runId={last.id} />
              {:else if last}
                <div class="mt-1 text-xs whitespace-nowrap text-muted tabular-nums" title={stamp(last.started_at)}>
                  {ago(last.started_at)} · {duration(last.started_at, last.finished_at)} · {bytes(last.bytes)}
                </div>
                {#if last.error && st.tone !== "ok" && st.tone !== "idle"}
                  <div class="mt-1 max-w-64 text-xs break-words text-bad">{last.error}</div>
                {/if}
              {/if}
            </td>
            <td class="px-3 py-2.5 max-md:hidden" title={stamp(b.next_run)}>
              <span class="whitespace-nowrap">{b.next_run ? ago(b.next_run) : "–"}</span>
              <div class="text-xs text-muted">{describeSchedule(b.schedule)}</div>
            </td>
            <td class="px-3 py-2.5">
              <div class="flex flex-wrap items-center justify-end gap-2">
                <button
                  type="button"
                  role="switch"
                  aria-checked={b.enabled}
                  aria-label="{b.name} turned on"
                  disabled={op.isPending}
                  onclick={() => setEnabled(b, !b.enabled)}
                  class="relative h-4 w-7 shrink-0 rounded-full transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:opacity-50 {b.enabled ? 'bg-accent' : 'bg-line'}"
                >
                  <span aria-hidden="true" class="absolute top-0.5 size-3 rounded-full bg-surface transition-[left] {b.enabled ? 'left-3.5' : 'left-0.5'}"></span>
                </button>
                <button type="button" class="btn btn-sm" disabled={b.running || op.isPending} onclick={() => runNow(b)}>Run now</button>
                <RowMenu
                  name={b.name}
                  items={[
                    { label: "Edit", onSelect: () => (form = { backup: b, preselect: [] }) },
                    { label: "Delete…", danger: true, disabled: b.running, onSelect: () => askDeleteBackup(b) },
                  ]}
                />
              </div>
            </td>
          </tr>
        {:else}
          <tr><td colspan="5" class="px-3 py-3 text-muted">Loading…</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<section id="destinations" class="mt-8 scroll-mt-20" aria-labelledby="destinations-heading">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h2 id="destinations-heading" class="min-w-0 flex-1 font-semibold">
      <Help label="Destinations">
        The places backups are copied to. At least one should be in another building, so a fire or a failed disk does not take the copies too.
      </Help>
    </h2>
    <button type="button" class="btn" onclick={() => (destForm = { destination: null })}>Add destination</button>
  </div>
  {#if destinations.data && dests.length === 0}
    <p class="mt-2 rounded-lg border border-line bg-surface px-4 py-3 text-muted">No destinations yet.</p>
  {:else}
    <div class="relative mt-2 overflow-x-auto rounded-lg border border-line bg-surface">
      <table class="w-full border-collapse text-left md:min-w-[760px]">
        <thead>
          <tr class="border-b border-line bg-sunken">
            <th scope="col" class="label px-3 py-2">Destination</th>
            <th scope="col" class="label px-3 py-2">State</th>
            <th scope="col" class="label px-3 py-2 max-md:hidden">Where</th>
            <th scope="col" class="label px-3 py-2 max-md:hidden">Used by</th>
            <th scope="col" class="px-3 py-2"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each dests as d (d.id)}
            {@const t = tests[d.id]}
            {@const st = destinationState(d, t)}
            <tr class="border-b border-line align-top last:border-0">
              <td class="px-3 py-2.5">
                {d.name}
                <div class="text-xs text-muted">
                  {KIND_LABEL[d.kind]}{#if d.kind === "restic" && !d.password_confirmed}{" · "}<span class="text-warn">password not saved</span>{/if}
                </div>
                <div class="text-xs break-all text-muted md:hidden">{destinationWhere(d)}</div>
              </td>
              <td class="px-3 py-2.5">
                <Pill tone={st.tone}>{st.label}</Pill>
                {#if t && t !== "running"}<div role="status" class="mt-1 max-w-64 text-xs break-words {t.ok ? 'text-muted' : 'text-bad'}">{t.detail}</div>{/if}
              </td>
              <td class="px-3 py-2.5 max-md:hidden">
                <span class="break-all">{destinationWhere(d)}</span>
                <div class="text-xs text-muted">
                  {#if d.kind === "restic"}
                    {describeKeep(d.keep)}
                  {:else}
                    {d.delete_removed ? "Deletes removed and skipped files" : "Keeps removed files"}
                  {/if}
                </div>
              </td>
              <td class="px-3 py-2.5 max-md:hidden">{d.used_by.join(", ") || "–"}</td>
              <td class="px-3 py-2.5">
                <div class="flex items-center justify-end gap-2">
                  <button type="button" class="btn btn-sm" onclick={() => (destForm = { destination: d })}>Edit</button>
                  <RowMenu
                    name={d.name}
                    items={[
                      { label: "Test", disabled: t === "running", onSelect: () => void test(d) },
                      ...(d.kind === "restic" ? [{ label: d.password_confirmed ? "Show password" : "Show and save password", onSelect: () => (password = { destination: d, generated: "" }) }] : []),
                      { label: "Duplicate", disabled: op.isPending, onSelect: () => duplicate(d) },
                      { label: d.used_by.length > 0 ? "Remove… (in use)" : "Remove…", danger: true, disabled: d.used_by.length > 0, onSelect: () => askDeleteDestination(d) },
                    ]}
                  />
                </div>
              </td>
            </tr>
          {:else}
            <tr><td colspan="5" class="px-3 py-3 text-muted">Loading…</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

{#if form}
  {#key form}
    <BackupForm
      backup={form.backup}
      preselect={form.preselect}
      coverage={coverage.data}
      destinations={dests}
      stackTitle={title}
      onSaved={() => {
        form = null;
        void refresh();
      }}
      onClose={() => (form = null)}
      onDelete={askDeleteBackup}
      onConfirmPassword={(d) => (password = { destination: d, generated: "" })}
    />
  {/key}
{/if}

{#if destForm}
  <DestinationForm destination={destForm.destination} onSaved={destinationSaved} onClose={() => (destForm = null)} />
{/if}

{#if password}
  <PasswordDialog
    destination={password.destination}
    generated={password.generated}
    onDone={() => {
      password = null;
      void refresh();
    }}
  />
{/if}

{#if confirm}
  {@const c = confirm}
  <Modal open={true} onOpenChange={(o) => !o && (confirm = null)} title={c.title} description={c.body}>
    {#if error}<p class="text-bad">{error}</p>{/if}
    {#snippet actions()}
      <button type="button" class="btn btn-danger" disabled={op.isPending} onclick={() => op.mutate(c.run)}>{c.label}</button>
    {/snippet}
  </Modal>
{/if}
