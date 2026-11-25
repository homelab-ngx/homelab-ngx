<script lang="ts">
  import type { Stack, StackUpdate } from "#lib/api/client.ts";
  import { duration, shortRev } from "#lib/format.ts";
  import { stackTitle, stepOf, updateSteps } from "#lib/stacks.ts";
  import AskButton from "./ask/AskButton.svelte";
  import Disclosure from "./Disclosure.svelte";
  import Pill from "./Pill.svelte";
  import Steps from "./Steps.svelte";

  interface Props {
    stack: Stack;
    /** The stack's latest attempt, for its package changes. */
    attempt?: StackUpdate;
  }
  let { stack, attempt }: Props = $props();

  const op = $derived(stack.operation!);
  const running = $derived(!op.finished_at);
  const stepped = $derived(op.kind === "start" || op.kind === "update" || op.kind === "rollback");
  const current = $derived(running ? stepOf(op.phase) : op.error ? stepOf(op.phase) : updateSteps.length);
  const title = $derived(
    {
      start: stack.generation <= 1 && attempt?.kind === "install" ? "Installing" : "Starting",
      stop: "Stopping",
      restart: "Restarting",
      update: "Updating",
      rollback: "Rolling back",
    }[op.kind] +
      ` ${stackTitle(stack)}` +
      (op.kind === "update" && stack.approved ? ` to ${shortRev(stack.approved.rev)}` : ""),
  );
  const changes = $derived(attempt && attempt.stack === stack.name ? attempt.changes : []);
  /** The failed attempt this operation ran, to ask about. */
  const failedAttempt = $derived(
    !running && op.error && attempt?.stack === stack.name && attempt.result !== "healthy" && attempt.result !== "running" ? attempt : undefined,
  );

  // The elapsed time moves while the operation runs.
  let now = $state(Date.now());
  $effect(() => {
    if (!running) return;
    const tick = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(tick);
  });
</script>

<section class="card" aria-live="polite" aria-label="{title} progress">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h3 class="font-semibold">{running ? title : `${title}: ${op.error ? "failed" : "done"}`}</h3>
    {#if running}
      <Pill tone="info">{op.phase}</Pill>
    {:else if op.error}
      <Pill tone="bad">Failed</Pill>
    {:else}
      <Pill tone="ok">{op.phase}</Pill>
    {/if}
  </div>
  {#if stepped}
    <div class="mt-3"><Steps steps={updateSteps} {current} failed={!running && !!op.error} /></div>
  {/if}
  <p class="mt-3 text-xs text-muted">
    {duration(op.started_at, op.finished_at, now)}
    {#if running && op.kind === "update"}· goes back to version {stack.generation} if it does not come up healthy{/if}
  </p>
  {#if op.error}
    <div class="mt-2 flex items-start gap-2">
      <p class="min-w-0 flex-1 text-bad">{op.error}</p>
      {#if failedAttempt}
        <AskButton small title="{stackTitle(stack)} {failedAttempt.kind}" subject={{ kind: "stack_update", ref: String(failedAttempt.id) }} />
      {/if}
    </div>
  {/if}
  {#if changes.length > 0}
    <Disclosure label="{changes.length} package change{changes.length === 1 ? '' : 's'}" class="mt-3">
      <ul class="text-xs">
        {#each changes as c (c.name)}
          <li>{c.name}: {c.from || "∅"} → {c.to || "∅"}</li>
        {/each}
      </ul>
    </Disclosure>
  {/if}
</section>
