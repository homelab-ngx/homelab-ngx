<script lang="ts">
  import type { DraftChange } from "#lib/api/client.ts";
  import { AssistRun, type AssistKind } from "#lib/assist.svelte.ts";
  import { usageLabel } from "#lib/assistant.ts";
  import { applyProposal, proposedChanges, type Buffer } from "#lib/drafts.ts";
  import AnswerParts from "#lib/components/ask/AnswerParts.svelte";
  import Help from "#lib/components/Help.svelte";
  import DiffView from "./DiffView.svelte";

  interface Props {
    name: string;
    /** The editor's files now. */
    buffer: Buffer;
    /** The editor's unsaved changes, which the assistant reads on top of the saved files. */
    unsaved: DraftChange[];
    /** The file the editor shows. */
    open: string;
    /** What the check and the last build found to fix; "" for nothing. */
    fixText: string;
    /** The model in use, for the panel's line about it. */
    model: string;
    /** Put files into the editor (nothing is saved). */
    onApply: (files: Buffer) => void;
  }
  let { name, buffer, unsaved, open, fixText, model, onApply }: Props = $props();

  /** What a request asks for; Fix error sends no words of its own. */
  const MODES: { kind: AssistKind; label: string; placeholder: string; needsText: boolean }[] = [
    { kind: "draft", label: "Draft", placeholder: "Describe the stack", needsText: true },
    { kind: "ask", label: "Ask", placeholder: "Question", needsText: true },
    { kind: "review", label: "Review", placeholder: "Focus (optional)", needsText: false },
    { kind: "fix", label: "Fix error", placeholder: "", needsText: false },
  ];

  const run = new AssistRun();
  let text = $state("");
  let kind = $state<AssistKind>("draft");
  let applied = $state(false);
  let lastKind = $state<AssistKind | null>(null);
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

  function askNow() {
    if (!canSend) return;
    lastKind = active;
    applied = false;
    void run.ask(name, active, fixing ? "" : text, $state.snapshot(unsaved), open);
  }

  function send(e: SubmitEvent) {
    e.preventDefault();
    askNow();
  }

  function textareaKeydown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
      e.preventDefault();
      askNow();
    }
  }

  function clear() {
    run.clear();
    applied = false;
    lastKind = null;
  }

  function apply(path?: string) {
    onApply(applyProposal(buffer, run.proposal, path));
    for (const p of path ? [path] : changes) run.settle(p);
    applied = true;
  }

  function discardAll() {
    for (const p of changes) run.settle(p);
  }
</script>

<section class="flex min-w-0 flex-col gap-3 p-3" aria-labelledby="assistant-h">
  <div class="flex items-baseline gap-2">
    <h2 id="assistant-h" class="min-w-0 font-semibold">
      <Help label="Assistant">
        Reads the file list, manifest, flake.nix, open and changed files, and Check and Build output. Proposes edits
        without saving. Never sees variable values.
      </Help>
    </h2>
    <span class="ml-auto truncate text-xs text-muted">{model}</span>
  </div>

  <form class="flex flex-col gap-2" onsubmit={send}>
    <fieldset class="grid grid-cols-2 gap-1.5">
      <legend class="sr-only">Request</legend>
      {#each MODES as m (m.kind)}
        {@const off = m.kind === "fix" && !fixText}
        <label
          class="flex items-center gap-2 rounded-md border border-line px-2.5 py-1.5 has-checked:border-accent has-checked:bg-accent-soft {off
            ? 'cursor-not-allowed opacity-50'
            : 'cursor-pointer'}"
          title={off ? "No check or build error to fix." : undefined}
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
        <textarea class="input min-h-24 resize-y text-muted" readonly value={fixText}></textarea>
      {:else}
        <textarea class="input min-h-24 resize-y" bind:value={text} onkeydown={textareaKeydown} maxlength="4000" placeholder={mode.placeholder}></textarea>
      {/if}
    </label>
    <div class="flex items-center gap-2">
      <button type="submit" class="btn btn-sm btn-primary" disabled={!canSend}>Send</button>
      {#if run.running}
        <button type="button" class="btn btn-sm" onclick={() => run.stop()}>Stop</button>
      {:else if run.answer || run.error}
        <button type="button" class="btn btn-sm ml-auto" onclick={clear}>Clear</button>
      {/if}
    </div>
  </form>

  {#if run.running}
    <div class="flex items-center gap-2 text-muted" role="status">
      <span class="size-2 rounded-full bg-accent motion-safe:animate-pulse" aria-hidden="true"></span>
      {run.text ? "Writing…" : "Thinking…"}
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
      <p class="rounded-md bg-warn-soft px-2.5 py-1.5 text-xs text-warn">{d}</p>
    {/each}
    {#if changes.length}
      <div class="flex flex-wrap items-center gap-2 border-t border-line pt-3">
        <p class="text-muted">Proposed changes</p>
        {#if changes.length > 1}
          <div class="ml-auto flex items-center gap-2">
            <button type="button" class="btn btn-sm" onclick={discardAll}>Discard all</button>
            <button type="button" class="btn btn-sm btn-primary" onclick={() => apply()}>Apply all</button>
          </div>
        {/if}
      </div>
      {#each changes as path (path)}
        <div class="flex flex-col gap-1.5">
          <div class="flex flex-wrap items-center gap-2">
            <span class="min-w-0 flex-1 truncate" title={path}>{path}{#if buffer[path] === undefined}<span class="text-muted"> · new</span>{/if}</span>
            <button type="button" class="btn btn-sm shrink-0 {changes.length === 1 ? 'btn-primary' : ''}" onclick={() => apply(path)}>Apply</button>
            <button type="button" class="btn btn-sm shrink-0" onclick={() => run.settle(path)}>Discard</button>
          </div>
          <DiffView file={path} original={buffer[path] ?? ""} proposed={run.proposal[path] ?? ""} label="Proposed {path}" />
        </div>
      {/each}
    {:else if applied}
      <p class="text-xs text-ok">Applied.</p>
    {:else if answered && !run.running && run.answer!.files.length === 0 && lastKind !== "ask"}
      <p class="text-xs text-muted">No proposed changes.</p>
    {/if}
    {#if usageLabel(run.answer.usage)}<p class="text-xs text-muted tabular-nums">{usageLabel(run.answer.usage)}</p>{/if}
  {/if}
</section>
