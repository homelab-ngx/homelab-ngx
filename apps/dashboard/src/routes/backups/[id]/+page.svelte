<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { api, call, describe } from "#lib/api/client.ts";
  import BackupForm from "#lib/components/backups/BackupForm.svelte";
  import PasswordDialog from "#lib/components/backups/PasswordDialog.svelte";
  import RunsDialog from "#lib/components/backups/RunsDialog.svelte";
  import RunStrip from "#lib/components/backups/RunStrip.svelte";
  import RunView from "#lib/components/backups/RunView.svelte";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { ago, stamp } from "#lib/format.ts";
  import { backupKeys, backupState, covers, dayMarks, describeSchedule, KIND_LABEL, type Backup, type Coverage, type Destination } from "#lib/backups.ts";

  const id = $derived(Number(page.params.id));
  const client = useQueryClient();

  // The list is the overview's own query: one read serves both pages, and it
  // carries the last 14 days of runs.
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

  const backup = $derived(backups.data?.find((b) => b.id === id) ?? null);
  const days = $derived(dayMarks(backup?.recent_runs ?? []));

  /** The run on show: the one the address names, else the newest (the one in flight, while one is). */
  const asked = $derived(Number(page.url.searchParams.get("run")) || null);
  const shown = $derived(asked ?? backup?.last_run?.id ?? null);
  const newest = $derived(shown !== null && shown === backup?.last_run?.id);
  const shownDay = $derived(days.find((d) => d.run?.id === shown) ?? null);

  const runHref = (run: number) => `/backups/${id}?run=${run}`;
  const open = (run: number) => goto(runHref(run), { replace: true, reset: false });

  let error = $state("");
  let editing = $state(false);
  let allRuns = $state(false);
  let deleting = $state(false);
  let password = $state<Destination | null>(null);

  /** Where a backup goes and when, in one line. */
  const whereWhen = (b: Backup) =>
    [b.destinations.map((d) => d.name).join(", ") || "No destination", describeSchedule(b.schedule), b.enabled ? "" : "off"].filter(Boolean).join(" · ");

  const refresh = () => client.invalidateQueries({ queryKey: backupKeys.all });
  const at = () => ({ params: { path: { id } } });

  const run = createMutation(() => ({
    mutationFn: () => call(api.POST("/backups/{id}/run", at())),
    onMutate: () => (error = ""),
    onSuccess: (started) => open(started.run_id),
    onError: (e: unknown) => (error = describe(e)),
    onSettled: refresh,
  }));
  const remove = createMutation(() => ({
    mutationFn: () => call(api.DELETE("/backups/{id}", at())),
    onMutate: () => (error = ""),
    onError: (e: unknown) => (error = describe(e)),
    onSuccess: async () => {
      deleting = false;
      // Leave first, so this page does not ask for a backup that is gone.
      await goto("/backups");
      await refresh();
    },
  }));
</script>

<svelte:head><title>{backup?.name ?? "Backup"} · Backups · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/backups">Backups</a> /</p>

{#if backups.error}
  <p role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(backups.error)}</p>
{:else if !backups.data}
  <p class="mt-3 text-muted">Loading…</p>
{:else if !backup}
  <div class="card mt-3">
    <h1 class="font-semibold">This backup does not exist</h1>
    <p class="mt-3"><a class="btn" href="/backups">Back to backups</a></p>
  </div>
{:else}
  {@const b = backup}
  {@const st = backupState(b)}
  <div class="mt-1 flex flex-wrap items-center justify-between gap-3">
    <h1 class="flex flex-wrap items-center gap-3 text-xl font-semibold">
      {b.name}
      <Pill tone={st.tone}>{st.label}</Pill>
    </h1>
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn" onclick={() => (editing = true)}>Edit</button>
      <button
        type="button"
        class="btn btn-primary"
        disabled={b.running || run.isPending}
        title={b.running ? "Running now" : ""}
        onclick={() => run.mutate()}
      >
        Run now
      </button>
    </div>
  </div>

  {#if error && !deleting}
    <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
  {/if}

  <section class="card mt-4" aria-labelledby="days-heading">
    <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-1">
      <h2 id="days-heading" class="min-w-0 flex-1 font-semibold">
        <Help label="Last 14 days">
          One mark per day. A full green bar is a good run, a shorter amber bar a partly failed one, a short red bar a failed one, an empty one a day
          without a run. A mark shows that day's run below.
        </Help>
      </h2>
      <p class="text-xs text-muted" title={stamp(b.next_run)}>
        {#if b.next_run}Next run {ago(b.next_run)}{:else if !b.enabled}Off{:else}Manual{/if}
      </p>
    </div>
    <div class="mt-3 flex flex-wrap items-end justify-between gap-3">
      <RunStrip {days} href={runHref} selected={shown} name={b.name} large />
      <button type="button" class="btn btn-sm" onclick={() => (allRuns = true)}>All runs</button>
    </div>
  </section>

  <section class="mt-5" aria-labelledby="run-heading">
    <h2 id="run-heading" class="font-semibold">
      {#if shown === null}Runs{:else if newest}Latest run{:else if shownDay}Run on {shownDay.label.split(":")[0]}{:else}Earlier run{/if}
    </h2>
    <div class="mt-2">
      {#if shown === null}
        <p class="rounded-lg border border-line bg-surface px-4 py-3 text-muted">
          This backup has not run yet.
        </p>
      {:else}
        {#key shown}<RunView runId={shown} />{/key}
      {/if}
    </div>
  </section>

  <!-- Looked at rarely: closed until opened, and an opened one stays open on this device. -->
  <section class="card mt-4" aria-label="Sources">
    <Disclosure section label="Sources" summary={covers(b.sources)} remember="backup-what">
      <ul class="divide-y divide-line">
        {#each b.sources as s (s.kind + s.stack + s.folder + s.path)}
          <li class="py-1.5">
            {#if s.kind === "core"}
              Server settings
            {:else if s.kind === "path"}
              <span class="break-all">{s.path}</span>
            {:else}
              {s.label}
              {#if s.host_path}<span class="text-xs break-all text-muted"> · {s.host_path}</span>{/if}
            {/if}
            {#if s.problem}<div class="text-xs text-warn">{s.problem}</div>{/if}
          </li>
        {:else}
          <li class="py-1.5 text-warn">No sources. Edit the backup to pick what to copy.</li>
        {/each}
      </ul>
    </Disclosure>
  </section>

  <section class="card mt-4" aria-label="Destinations and schedule">
    <Disclosure section label="Destinations and schedule" summary={whereWhen(b)} remember="backup-where">
      <ul class="divide-y divide-line">
        {#each b.destinations as d (d.id)}
          <li class="flex flex-wrap items-center gap-x-3 py-1.5">
            <span class="min-w-0 flex-1">{d.name} <span class="text-muted">· {KIND_LABEL[d.kind]}</span></span>
            {#if d.ready}<Pill tone="ok">Ready</Pill>{:else}<Pill tone="warn">Skipped</Pill>{/if}
            {#if !d.ready}<span class="basis-full text-xs text-warn">Runs skip it until its password is saved.</span>{/if}
          </li>
        {:else}
          <li class="py-1.5 text-warn">No destination. Edit the backup to pick one.</li>
        {/each}
        <li class="py-1.5">{describeSchedule(b.schedule)}{#if !b.enabled}<span class="text-muted"> · off</span>{/if}</li>
      </ul>
    </Disclosure>
  </section>

  {#if editing}
    <BackupForm
      backup={b}
      coverage={coverage.data}
      destinations={destinations.data ?? []}
      onSaved={() => {
        editing = false;
        void refresh();
      }}
      onClose={() => (editing = false)}
      onDelete={() => {
        editing = false;
        deleting = true;
      }}
      onConfirmPassword={(d) => (password = d)}
    />
  {/if}

  {#if allRuns}
    <RunsDialog
      backup={b}
      onSelect={(r) => {
        allRuns = false;
        void open(r);
      }}
      onClose={() => (allRuns = false)}
    />
  {/if}

  {#if password}
    <PasswordDialog
      destination={password}
      generated=""
      onDone={() => {
        password = null;
        void refresh();
      }}
    />
  {/if}

  {#if deleting}
    <Modal
      open={true}
      onOpenChange={(o) => !o && (deleting = false)}
      title="Delete {b.name}?"
      description="The backup stops running. Files already copied stay at its destinations."
    >
      {#if error}<p class="text-bad">{error}</p>{/if}
      {#snippet actions()}
        <button type="button" class="btn btn-danger" disabled={remove.isPending} onclick={() => remove.mutate()}>Delete backup</button>
      {/snippet}
    </Modal>
  {/if}
{/if}
