<script lang="ts">
  import type { Finding } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { SEVERITY } from "#lib/security.ts";

  interface Props {
    finding: Finding;
    onClose: () => void;
  }
  let { finding, onClose }: Props = $props();
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title={finding.title} wide>
  <div class="flex flex-col gap-4">
    <div class="flex flex-wrap items-center gap-3">
      <Pill tone={SEVERITY[finding.severity]}>{finding.severity}</Pill>
      {#if !finding.verified}<Pill tone="warn">unverified</Pill>{/if}
    </div>
    {#if !finding.verified}
      <p class="text-warn">The quoted evidence is not in what the model was given. Check it before acting.</p>
    {/if}
    <div>
      <h3 class="label">Evidence</h3>
      <pre class="mt-1 overflow-auto rounded-md bg-sunken px-3 py-2 font-mono text-xs whitespace-pre-wrap">{finding.evidence || "–"}</pre>
    </div>
    <div>
      <h3 class="label">Risk</h3>
      <p class="mt-1">{finding.risk || "–"}</p>
    </div>
    <div>
      <h3 class="label">Fix</h3>
      <p class="mt-1">{finding.fix || "–"}</p>
    </div>
  </div>
</Modal>
