<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { api, call, describe } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { bytes, duration, stamp } from "#lib/format.ts";
  import { backupKeys, RUN_STATE, TRIGGER, type Backup } from "#lib/backups.ts";

  interface Props {
    backup: Backup;
    /** The operator picked a run to look at. */
    onSelect: (runId: number) => void;
    onClose: () => void;
  }
  let { backup, onSelect, onClose }: Props = $props();

  const runs = createQuery(() => ({
    queryKey: backupKeys.runs(backup.id),
    queryFn: () => call(api.GET("/backups/{id}/runs", { params: { path: { id: backup.id }, query: { limit: 200 } } })),
    refetchInterval: (q) => (q.state.data?.some((r) => r.result === "running") ? 3_000 : 15_000),
  }));
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title="All runs of {backup.name}" wide>
  {#if runs.error}
    <p class="text-bad">{describe(runs.error)}</p>
  {:else if runs.data}
    <table class="w-full border-collapse text-left">
      <thead>
        <tr class="border-b border-line">
          <th scope="col" class="label py-1.5 pr-3">Started</th>
          <th scope="col" class="label py-1.5 pr-3 max-sm:hidden">Trigger</th>
          <th scope="col" class="label py-1.5 pr-3">Result</th>
          <th scope="col" class="label py-1.5 pr-3 text-right max-sm:hidden">Took</th>
          <th scope="col" class="label py-1.5 pr-3 text-right max-sm:hidden">Files sent</th>
          <th scope="col" class="label py-1.5 pr-3 text-right">Size sent</th>
          <th scope="col" class="py-1.5"><span class="sr-only">Open</span></th>
        </tr>
      </thead>
      <tbody>
        {#each runs.data as r (r.id)}
          {@const st = RUN_STATE[r.result]}
          <tr class="border-b border-line align-baseline last:border-0">
            <td class="py-1.5 pr-3 whitespace-nowrap">{stamp(r.started_at)}</td>
            <td class="py-1.5 pr-3 text-muted max-sm:hidden">{TRIGGER[r.trigger]}</td>
            <td class="py-1.5 pr-3">
              <Pill tone={st.tone}>{st.label}</Pill>
              {#if r.error}<div class="mt-1 text-xs break-words text-bad">{r.error}</div>{/if}
            </td>
            <td class="py-1.5 pr-3 text-right whitespace-nowrap tabular-nums max-sm:hidden">{duration(r.started_at, r.finished_at)}</td>
            <td class="py-1.5 pr-3 text-right tabular-nums max-sm:hidden">{r.files}</td>
            <td class="py-1.5 pr-3 text-right whitespace-nowrap tabular-nums">{bytes(r.bytes)}</td>
            <td class="py-1.5 text-right">
              <button type="button" class="btn btn-sm" aria-label="Open the run started {stamp(r.started_at)}" onclick={() => onSelect(r.id)}>Open</button>
            </td>
          </tr>
        {:else}
          <tr><td colspan="7" class="py-2 text-muted">This backup has not run yet.</td></tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="text-muted">Loading…</p>
  {/if}
</Modal>
