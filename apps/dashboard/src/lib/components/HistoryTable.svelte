<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { ChevronRight } from "@lucide/svelte";
  import { describe, type Stack, type StackUpdate } from "#lib/api/client.ts";
  import { busyPoll, fetchUpdate, keys } from "#lib/api/queries.ts";
  import { shortRev, stamp } from "#lib/format.ts";
  import { stackTitle } from "#lib/stacks.ts";
  import AskButton from "./ask/AskButton.svelte";
  import Disclosure from "./Disclosure.svelte";
  import Help from "./Help.svelte";
  import Pill from "./Pill.svelte";

  interface Props {
    stack: Stack;
    /** The stack's install, update and rollback attempts, newest first. */
    attempts: StackUpdate[];
    /** A stack operation runs: rollbacks wait. */
    working: boolean;
    onRollback: (generation: number) => void;
  }
  let { stack, attempts, working, onRollback }: Props = $props();

  /** The attempt whose log is open below its row. */
  let shown = $state<number | null>(null);

  const log = createQuery(() => ({
    queryKey: keys.update(shown ?? 0),
    queryFn: () => fetchUpdate(shown!),
    enabled: shown !== null,
    refetchInterval: (q) => (q.state.data?.result === "running" ? busyPoll : false),
  }));

  /** Generations and the attempts that built none, newest first. */
  const rows = $derived.by(() => {
    const byGen = new Map(attempts.filter((a) => a.kind !== "rollback").map((a) => [a.to_generation, a]));
    const out = stack.generations.map((g) => ({
      key: `g${g.id}`,
      generation: g.id as number | null,
      rev: g.rev,
      at: g.built_at,
      failed: g.failed,
      attempt: byGen.get(g.id),
    }));
    for (const a of attempts) {
      if (a.to_generation === 0 && a.result !== "running") {
        out.push({ key: `a${a.id}`, generation: null, rev: a.rev, at: a.started_at, failed: true, attempt: a });
      }
    }
    return out.sort((x, y) => y.at.localeCompare(x.at));
  });

  const toggle = (id: number) => (shown = shown === id ? null : id);
</script>

<!-- A stack's versions and the attempts that built none (DESIGN.md, Stack
     Page: the History tab). -->
<div>
  <p class="mb-2">
    <Help label="Versions" class="label">
      Every install and update builds a version (a generation): a complete build of the stack at one commit. Rolling back switches
      to that build again; nothing is rebuilt and data is not touched.
    </Help>
  </p>
  <div class="relative overflow-x-auto rounded-md border border-line">
    <table class="w-full min-w-[560px] border-collapse text-left tabular-nums">
      <thead>
        <tr class="border-b border-line bg-sunken">
          <th scope="col" class="label px-3 py-2">Version</th>
          <th scope="col" class="label px-3 py-2">Commit</th>
          <th scope="col" class="label px-3 py-2">Built</th>
          <th scope="col" class="label px-3 py-2">Result</th>
          <th scope="col" class="px-3 py-2"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        {#each rows as row (row.key)}
          <tr class="border-b border-line last:border-0">
            <td class="px-3 py-2 whitespace-nowrap">
              {#if row.generation === null}
                <span class="text-muted">–</span>
              {:else}
                {row.generation}
                {#if row.generation === stack.generation}<Pill tone="ok">running</Pill>{/if}
              {/if}
            </td>
            <td class="px-3 py-2">{shortRev(row.rev)}</td>
            <td class="px-3 py-2 whitespace-nowrap">{stamp(row.at)}</td>
            <td class="px-3 py-2">
              {#if row.attempt?.result === "build_failed"}
                <Pill tone="bad">Build failed</Pill>
              {:else if row.failed || row.attempt?.result === "rolled_back"}
                <Pill tone="bad">Rolled back</Pill>
                {#if row.attempt?.reason}<span class="ml-1 text-xs text-muted">{row.attempt.reason.split(";")[0]}</span>{/if}
              {:else if row.attempt?.result === "failed"}
                <Pill tone="bad">Failed</Pill>
              {:else if row.attempt?.result === "running"}
                <Pill tone="info">Running</Pill>
              {:else}
                <span class="text-muted">Healthy</span>
              {/if}
            </td>
            <td class="px-3 py-2 text-right whitespace-nowrap">
              {#if row.generation !== null && row.generation !== stack.generation && !row.failed}
                <button type="button" class="btn btn-sm" disabled={working} onclick={() => onRollback(row.generation!)}>
                  Roll back to {row.generation}
                </button>
              {/if}
              {#if row.attempt}
                {@const a = row.attempt}
                <button
                  type="button"
                  class="btn btn-sm"
                  aria-expanded={shown === a.id}
                  aria-controls="attempt-log-{a.id}"
                  onclick={() => toggle(a.id)}
                >
                  <ChevronRight class="size-3 transition-transform {shown === a.id ? 'rotate-90' : ''}" aria-hidden="true" />
                  Log
                </button>
              {/if}
            </td>
          </tr>
          {#if row.attempt && shown === row.attempt.id}
            <tr id="attempt-log-{row.attempt.id}" class="border-b border-line bg-sunken last:border-0">
              <td colspan="5" class="px-3 py-2">
                {#if log.error}
                  <p class="text-bad">{describe(log.error)}</p>
                {:else if log.data}
                  {#if log.data.reason || (log.data.result !== "healthy" && log.data.result !== "running")}
                    <div class="mb-2 flex items-start gap-2">
                      <p class="min-w-0 flex-1 text-bad">{log.data.reason}</p>
                      {#if log.data.result !== "healthy" && log.data.result !== "running"}
                        <AskButton small title="{stackTitle(stack)} {log.data.kind}" subject={{ kind: "stack_update", ref: String(log.data.id) }} />
                      {/if}
                    </div>
                  {/if}
                  {#if log.data.changes.length > 0}
                    <Disclosure label="{log.data.changes.length} package change{log.data.changes.length === 1 ? '' : 's'}" class="mb-2">
                      <ul class="text-xs">
                        {#each log.data.changes as c (c.name)}<li>{c.name}: {c.from || "∅"} → {c.to || "∅"}</li>{/each}
                      </ul>
                    </Disclosure>
                  {/if}
                  <pre class="max-h-72 overflow-auto font-mono text-xs leading-relaxed whitespace-pre-wrap">{log.data.log || "No log."}</pre>
                {:else}
                  <p class="text-muted">Loading…</p>
                {/if}
              </td>
            </tr>
          {/if}
        {:else}
          <tr><td colspan="5" class="px-3 py-3 text-muted">Nothing built yet.</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>
