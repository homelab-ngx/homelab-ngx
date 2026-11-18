<script lang="ts">
  import { onMount } from "svelte";
  import { AskRun, type AskSubject } from "#lib/ask.svelte.ts";
  import { usageLabel } from "#lib/assistant.ts";
  import Modal from "#lib/components/Modal.svelte";
  import AnswerParts from "./AnswerParts.svelte";

  interface Props {
    /** What is asked about, in a few words: "Caddy logs". */
    title: string;
    subject: AskSubject;
    onClose: () => void;
  }
  let { title, subject, onClose }: Props = $props();

  const run = new AskRun();
  let question = $state("");
  let thinkBox = $state<HTMLPreElement>();
  // The reasoning follows its newest line.
  $effect(() => {
    if (run.thinking && thinkBox) thinkBox.scrollTop = thinkBox.scrollHeight;
  });
  const formId = $props.id();

  // The default question goes out as the dialog opens; it stops when it closes.
  onMount(() => {
    void run.ask(subject);
    return () => run.stop();
  });

  const meta = $derived(run.answer ? [run.answer.model, usageLabel(run.answer.usage)].filter(Boolean).join(" · ") : "");

  function submit(e: SubmitEvent) {
    e.preventDefault();
    void run.ask(subject, question);
  }
</script>

<Modal open onOpenChange={(o) => !o && onClose()} title="Ask · {title}" wide>
  <div class="flex flex-col gap-3" aria-live="polite">
    {#if run.running}
      <div class="flex items-center gap-2 text-muted" role="status">
        <span class="size-2 animate-pulse rounded-full bg-accent" aria-hidden="true"></span>
        {run.text ? "Writing…" : "Thinking…"}
        <button type="button" class="link ml-auto text-xs" onclick={() => run.stop()}>Stop</button>
      </div>
    {/if}
    {#if run.running && run.thinking && !run.text}
      <pre bind:this={thinkBox} class="max-h-40 overflow-auto rounded-md bg-sunken p-2 font-mono text-xs leading-relaxed whitespace-pre-wrap text-muted italic">{run.thinking}</pre>
    {/if}
    {#if run.answer}
      <AnswerParts
        summary={run.answer.summary}
        detail={{ label: "Cause", text: run.answer.cause }}
        lists={[{ label: "Steps", items: run.answer.steps, ordered: true }]}
        notes={run.answer.notes}
      />
    {:else if run.text}
      <pre class="max-h-64 overflow-auto rounded-md bg-sunken p-2 font-mono text-xs leading-relaxed whitespace-pre-wrap text-muted">{run.text}</pre>
    {/if}
    {#if run.error}
      <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{run.error}</p>
    {/if}
    {#if meta}<p class="text-xs text-muted tabular-nums">{meta}</p>{/if}
    <form id={formId} class="border-t border-line pt-3" onsubmit={submit}>
      <label>
        <span class="sr-only">Question</span>
        <input class="input" bind:value={question} maxlength="2000" placeholder="Another question" />
      </label>
    </form>
  </div>
  {#snippet actions()}
    <button type="submit" form={formId} class="btn btn-primary" disabled={run.running}>Ask</button>
  {/snippet}
</Modal>
