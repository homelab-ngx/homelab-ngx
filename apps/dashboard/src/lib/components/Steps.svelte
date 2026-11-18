<script lang="ts">
  interface Props {
    steps: readonly string[];
    /** Index of the step in progress; steps.length when all are done. */
    current: number;
    failed?: boolean;
  }
  let { steps, current, failed = false }: Props = $props();
</script>

<ol class="flex flex-col gap-1">
  {#each steps as step, i (step)}
    {@const done = i < current}
    {@const now = i === current}
    <li class="flex items-baseline gap-2.5 {done ? 'text-fg' : now ? 'font-semibold' : 'text-muted'}">
      <span aria-hidden="true" class="w-4 text-center {done ? 'text-ok' : now && failed ? 'text-bad' : 'text-accent'}">
        {done ? "✓" : now ? (failed ? "✕" : "◌") : "·"}
      </span>
      <span>{step}</span>
      <span class="sr-only">{done ? "done" : now ? (failed ? "failed" : "in progress") : "waiting"}</span>
    </li>
  {/each}
</ol>
