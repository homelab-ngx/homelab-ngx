<script lang="ts">
  import type { ServiceInfo } from "#lib/api/client.ts";
  import { logUnits } from "#lib/stacks.ts";
  import LogViewer from "./LogViewer.svelte";
  import Modal from "./Modal.svelte";

  interface Props {
    stack: string;
    /** What the stack is called on the dashboard; its name when absent. */
    title?: string;
    /** The stack's services, which the picker offers. */
    services: ServiceInfo[];
    /** The service to show first (its log id); null closes the dialog. */
    unit: string | null;
    onClose: () => void;
  }
  let { stack, title, services, unit, onClose }: Props = $props();

  const units = $derived(logUnits(stack, services));
</script>

<Modal open={unit !== null} onOpenChange={(o) => !o && onClose()} title="{title ?? stack} logs" wide>
  {#if unit !== null}
    {#key unit}
      <LogViewer {units} initial={unit} boxed />
    {/key}
  {/if}
</Modal>
