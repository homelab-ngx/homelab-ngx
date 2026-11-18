<script lang="ts">
  import type { DraftFiles } from "#lib/api/client.ts";
  import { AssistRun, type AssistKind } from "#lib/assist.svelte.ts";
  import { usageLabel } from "#lib/assistant.ts";
  import { applyProposal, proposedChanges } from "#lib/drafts.ts";
  import { FILES } from "#lib/editor.ts";
  import AnswerParts from "#lib/components/ask/AnswerParts.svelte";
  import Help from "#lib/components/Help.svelte";
  import DiffView from "./DiffView.svelte";

  interface Props {
    name: string;
    /** The editor's files now. */
    buffer: DraftFiles;
    /** What the check and the last build found to fix; "" for nothing. */
    fixText: string;
    /** The model in use, for the panel's line about it. */
    model: string;
    /** Put files into the editor (nothing is saved). */
    onApply: (files: DraftFiles) => void;
  }
  let { name, buffer, fixText, model, onApply }: Props = $props();

  /** What a request asks for; Fix last error sends no words of its own. */
  const MODES: { kind: AssistKind; label: string; placeholder: string; needsText: boolean }[] = [
    { kind: "draft", label: "Draft a new stack", placeholder: "Describe the stack", needsText: true },
    { kind: "ask", label: "Ask a question", placeholder: "Question", needsText: true },
    { kind: "review", label: "Review the draft", placeholder: "What to look at (optional)", needsText: false },
    { kind: "fix", label: "Fix last error", placeholder: "", needsText: false },
  ];

  const run = new AssistRun();
  let text = $state("");
  let kind = $state<AssistKind>("draft");
  let streamBox = $state<HTMLPreElement>();
  let thinkBox = $state<HTMLPreElement>();

  /** The chosen mode, or drafting once nothing is left to fix. */
  const active = $derived(kind === "fix" && !fixText ? "draft" : kind);
  const mode = $derived(MODES.find((m) => m.kind === active)!);
  const fixing = $derived(active === "fix");
  const canSend = $derived(!run.running && (fixing ? !!fixText : !mode.needsText || !!text.trim()));

  const changes = $derived(proposedChanges(buffer, run.proposal));
  const answered = $derived(!!run.answer);

  // A question in flight ends with the page.
  $effect(() => () => run.stop());
  // The streamed text and reasoning follow their newest line.
  $effect(() => {
    if (run.text && streamBox) streamBox.scrollTop = streamBox.scrollHeight;
  });
  $effect(() => {
    if (run.thinking && thinkBox) thinkBox.scrollTop = thinkBox.scrollHeight;
  });

  function send(e: SubmitEvent) {
    e.preventDefault();
    if (canSend) void run.ask(name, active, fixing ? "" : text, $state.snapshot(buffer) as DraftFiles);
  }

  function apply(key?: (typeof FILES)[number]["key"]) {
    onApply(applyProposal(buffer, run.proposal, key));
    for (const k of key ? [key] : changes) run.settle(k);
  }
</script>

<section class="flex min-w-0 flex-col gap-3 p-3" aria-labelledby="assistant-h">
  <div class="flex items-baseline gap-2">
    <h2 id="assistant-h" class="min-w-0 font-semibold">
      <Help label="Assistant">
        Reads the two files in the editor and what Check and the last Build said, then proposes changes to apply. Nothing is
        saved before Save. It never sees the stack's variable values.
      </Help>
    </h2>
    <span class="ml-auto truncate text-xs text-muted">{model}</span>
  </div>

  <form class="flex flex-col gap-2" onsubmit={send}>
    <fieldset class="flex flex-wrap gap-2">
      <legend class="sr-only">Request</legend>
      {#each MODES as m (m.kind)}
        {@const off = m.kind === "fix" && !fixText}
        <label
          class="flex items-center gap-2 rounded-md border border-line px-3 py-1.5 has-checked:border-accent has-checked:bg-accent-soft {off
            ? 'cursor-not-allowed opacity-50'
            : 'cursor-pointer'}"
          title={off ? "No failed check or build to fix." : undefined}
        >
          <input
            type="radio"
            name="assist-kind"
            value={m.kind}
            checked={active === m.kind}
            onchange={() => (kind = m.kind)}
            disabled={off}
            class="accent-accent"
          />
          {m.label}
        </label>
      {/each}
    </fieldset>
    <label class="flex flex-col gap-1">
      <span class="sr-only">{fixing ? "Error to fix" : mode.label}</span>
      {#if fixing}
        <textarea class="input min-h-32 resize-y text-muted" readonly value={fixText}></textarea>
      {:else}
        <textarea class="input min-h-32 resize-y" bind:value={text} maxlength="4000" placeholder={mode.placeholder}></textarea>
      {/if}
    </label>
    <div>
      <button type="submit" class="btn btn-sm btn-primary" disabled={!canSend}>Send</button>
    </div>
  </form>

  {#if run.running}
    <div class="flex items-center gap-2 text-muted" role="status">
      <span class="size-2 animate-pulse rounded-full bg-accent" aria-hidden="true"></span>
      {run.text ? "Writing…" : "Thinking…"}
      <button type="button" class="link ml-auto text-xs" onclick={() => run.stop()}>Stop</button>
    </div>
    {#if run.thinking && !run.text}
      <pre bind:this={thinkBox} class="max-h-32 overflow-auto rounded-md bg-sunken p-2 font-mono text-xs leading-relaxed whitespace-pre-wrap text-muted italic">{run.thinking}</pre>
    {/if}
    {#if run.text}
      <pre bind:this={streamBox} class="max-h-96 overflow-auto rounded-md bg-sunken p-2 font-mono text-xs leading-relaxed whitespace-pre-wrap text-muted">{run.text}</pre>
    {/if}
  {/if}

  {#if run.error}
    <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{run.error}</p>
  {/if}

  {#if run.answer}
    <AnswerParts
      summary={run.answer.summary}
      lists={[
        { label: "Changes", items: run.answer.changes },
        { label: "To do", items: run.answer.todo },
      ]}
      notes={run.answer.notes}
    />
    {#each run.answer.dropped as d, i (i)}
      <p class="text-xs text-warn">{d}</p>
    {/each}
    {#if changes.length}
      <div class="flex flex-wrap items-center gap-2 border-t border-line pt-3">
        <p class="text-muted">Proposed changes</p>
        {#if changes.length > 1}
          <button type="button" class="btn btn-sm btn-primary ml-auto" onclick={() => apply()}>Apply all</button>
        {/if}
      </div>
      {#each FILES.filter((f) => changes.includes(f.key)) as f (f.key)}
        <div class="flex flex-col gap-1.5">
          <div class="flex items-center gap-2">
            <span>{f.path}</span>
            <button type="button" class="btn btn-sm btn-primary ml-auto" onclick={() => apply(f.key)}>Apply</button>
            <button type="button" class="btn btn-sm" onclick={() => run.settle(f.key)}>Discard</button>
          </div>
          <DiffView file={f.key} original={buffer[f.key]} proposed={run.proposal[f.key]!} label="Proposed {f.path}" />
        </div>
      {/each}
    {:else if answered && !run.running}
      <p class="text-xs text-muted">No proposed changes.</p>
    {/if}
    {#if usageLabel(run.answer.usage)}<p class="text-xs text-muted tabular-nums">{usageLabel(run.answer.usage)}</p>{/if}
  {/if}
</section>
