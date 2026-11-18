<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { api, call, describe } from "#lib/api/client.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { bytes, duration, stamp } from "#lib/format.ts";
  import { backupKeys, byDestination, ITEM_STATE, RUN_STATE, TRIGGER, type RunDetail, type RunItem } from "#lib/backups.ts";
  import AskButton from "#lib/components/ask/AskButton.svelte";
  import RunProgress from "./RunProgress.svelte";

  interface Props {
    runId: number;
  }
  let { runId }: Props = $props();

  let showLog = $state(false);

  const run = createQuery(() => ({
    queryKey: backupKeys.run(runId),
    queryFn: () => call(api.GET("/backup-runs/{run}", { params: { path: { run: runId } } })) as Promise<RunDetail>,
    refetchInterval: (q) => (q.state.data?.result === "running" ? 2_500 : false),
  }));
  const r = $derived(run.data);
  const groups = $derived(byDestination(r?.items ?? []));
  const logs = createQuery(() => ({
    queryKey: backupKeys.logs(r?.backup_id ?? 0),
    queryFn: () => call(api.GET("/backups/{id}/logs", { params: { path: { id: r!.backup_id! } } })),
    enabled: showLog && !!r?.backup_id,
  }));

  const took = (it: RunItem) => (it.started_at ? duration(it.started_at, it.finished_at) : "–");
</script>

{#if run.error}
  <p class="text-bad">{describe(run.error)}</p>
{:else if !r}
  <p class="text-muted">Loading…</p>
{:else}
  {@const st = RUN_STATE[r.result]}
  <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
    <Pill tone={st.tone}>{st.label}</Pill>
    <p class="text-muted tabular-nums">
      {stamp(r.started_at)} · {TRIGGER[r.trigger]} · {duration(r.started_at, r.finished_at)} · {r.files} files · {bytes(r.bytes)}
    </p>
    {#if r.result === "failure" || r.result === "partial" || r.result === "unknown"}
      <span class="ml-auto"><AskButton small title="{r.backup_name} run" subject={{ kind: "backup_run", ref: String(r.id) }} /></span>
    {/if}
  </div>
  {#if r.error}
    <p class="mt-2 rounded-md bg-bad-soft px-3 py-2 break-words text-bad">{r.error}</p>
  {/if}
  {#if r.result === "running"}
    <div class="mt-3 rounded-md border border-line px-3 py-2">
      <RunProgress run={r} />
    </div>
  {/if}

  {#each groups as g (g.name)}
    <section class="mt-4" aria-label={g.name}>
      <div class="flex flex-wrap items-center gap-2">
        <h3 class="font-semibold">{g.name}</h3>
        {#if g.destination && g.destination.result !== "success"}
          {@const ds = ITEM_STATE[g.destination.result]}
          <Pill tone={ds.tone}>{ds.label}</Pill>
        {/if}
      </div>
      {#if g.destination?.error}
        <p class="mt-1 break-words text-bad">{g.destination.error}</p>
      {:else if g.destination?.note}
        <p class="mt-1 text-muted">{g.destination.note}</p>
      {/if}
      {#if g.folders.length > 0}
        <div class="mt-2 overflow-x-auto rounded-lg border border-line">
          <table class="w-full border-collapse text-left">
            <thead>
              <tr class="border-b border-line bg-sunken">
                <th scope="col" class="label px-3 py-1.5">Folder</th>
                <th scope="col" class="label px-3 py-1.5">Result</th>
                <th scope="col" class="label px-3 py-1.5 text-right max-sm:hidden">Files sent</th>
                <th scope="col" class="label px-3 py-1.5 text-right">Size sent</th>
                <th scope="col" class="label px-3 py-1.5 text-right max-sm:hidden">Took</th>
              </tr>
            </thead>
            <tbody>
              {#each g.folders as it (it.label + it.path)}
                {@const is = ITEM_STATE[it.result]}
                <tr class="border-b border-line align-baseline last:border-0">
                  <td class="px-3 py-1.5">
                    {#if it.label === it.path}
                      <span class="break-all">{it.path}</span>
                    {:else}
                      {it.label}
                      <div class="text-xs break-all text-muted">{it.path}</div>
                    {/if}
                    {#if it.error}<div class="mt-0.5 text-xs break-words text-bad">{it.error}</div>{/if}
                    {#if it.note}<div class="mt-0.5 text-xs text-muted">{it.note}</div>{/if}
                  </td>
                  <td class="px-3 py-1.5"><Pill tone={is.tone}>{is.label}</Pill></td>
                  <td class="px-3 py-1.5 text-right tabular-nums max-sm:hidden">{it.files}</td>
                  <td class="px-3 py-1.5 text-right whitespace-nowrap tabular-nums">{bytes(it.bytes)}</td>
                  <td class="px-3 py-1.5 text-right whitespace-nowrap tabular-nums max-sm:hidden">{took(it)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>
  {:else}
    {#if r.result !== "running"}
      <p class="mt-3 text-muted">This run has no folder results.</p>
    {/if}
  {/each}

  {#if r.backup_id}
    <!-- The lines are read only while the fold is open. -->
    <Disclosure label="Log lines" summary="Newest of this backup, earlier runs included" bind:open={showLog} class="mt-4">
      {#if logs.error}
        <p class="text-bad">{describe(logs.error)}</p>
      {:else if logs.data}
        <pre class="max-h-[40vh] overflow-auto rounded-md border border-line bg-sunken p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap">{logs.data.logs || "No log lines yet."}</pre>
      {:else if showLog}
        <p class="text-muted">Loading…</p>
      {/if}
    </Disclosure>
  {/if}
{/if}
