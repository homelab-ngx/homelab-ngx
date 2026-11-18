<script lang="ts">
  interface Part {
    label: string;
    items: string[];
    /** Steps to take in order: numbered. */
    ordered?: boolean;
  }
  interface Props {
    summary: string;
    /** A short paragraph under its label, such as the cause. */
    detail?: { label: string; text: string };
    lists?: Part[];
    /** What the model said outside the parts: all of it from a model that ignored them. */
    notes?: string;
  }
  let { summary, detail, lists = [], notes = "" }: Props = $props();
</script>

<!-- An assistant's answer in its parts (DESIGN.md, Ask): the summary, then
     short labelled lists; anything else follows as plain text. -->
<div class="flex flex-col gap-3 leading-relaxed">
  {#if summary}<p class="font-medium">{summary}</p>{/if}
  {#if detail?.text}
    <div>
      <h3 class="label">{detail.label}</h3>
      <p class="mt-0.5 break-words">{detail.text}</p>
    </div>
  {/if}
  {#each lists.filter((l) => l.items.length > 0) as l (l.label)}
    <div>
      <h3 class="label">{l.label}</h3>
      <svelte:element this={l.ordered ? "ol" : "ul"} class="mt-0.5 flex flex-col gap-1 pl-5 {l.ordered ? 'list-decimal' : 'list-disc'}">
        {#each l.items as it, i (i)}<li class="break-words">{it}</li>{/each}
      </svelte:element>
    </div>
  {/each}
  {#if notes}<div class="break-words whitespace-pre-wrap {summary ? 'text-muted' : ''}">{notes}</div>{/if}
</div>
