<script lang="ts">
  import type { SecurityAuditItem } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { stamp } from "#lib/format.ts";
  import { costLabel, countsLabel, POSTURE, RESULT } from "#lib/security.ts";

  interface Props {
    audits: SecurityAuditItem[];
    onSelect: (id: number) => void;
    onClose: () => void;
  }
  let { audits, onSelect, onClose }: Props = $props();
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title="Past audits" wide>
  <table class="w-full border-collapse text-left">
    <thead>
      <tr class="border-b border-line">
        <th scope="col" class="label py-1.5 pr-3">Started</th>
        <th scope="col" class="label py-1.5 pr-3">Result</th>
        <th scope="col" class="label py-1.5 pr-3">Posture</th>
        <th scope="col" class="label py-1.5 pr-3 max-sm:hidden">Findings</th>
        <th scope="col" class="label py-1.5 pr-3 text-right max-sm:hidden">Cost</th>
        <th scope="col" class="py-1.5"><span class="sr-only">Open</span></th>
      </tr>
    </thead>
    <tbody>
      {#each audits as a (a.id)}
        <tr class="border-b border-line align-baseline last:border-0">
          <td class="py-1.5 pr-3 whitespace-nowrap">{stamp(a.started_at)}</td>
          <td class="py-1.5 pr-3"><Pill tone={RESULT[a.result].tone}>{RESULT[a.result].label}</Pill></td>
          <td class="py-1.5 pr-3">
            {#if a.posture}<Pill tone={POSTURE[a.posture]}>{a.posture}</Pill>{:else}<span class="text-muted">–</span>{/if}
          </td>
          <td class="py-1.5 pr-3 text-muted max-sm:hidden">{countsLabel(a.counts)}</td>
          <td class="py-1.5 pr-3 text-right whitespace-nowrap text-muted tabular-nums max-sm:hidden">{costLabel(a.usage)}</td>
          <td class="py-1.5 text-right">
            <button type="button" class="btn btn-sm" aria-label="Open the audit started {stamp(a.started_at)}" onclick={() => onSelect(a.id)}>Open</button>
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</Modal>
