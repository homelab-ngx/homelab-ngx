<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { fetchAssistant, keys } from "#lib/api/queries.ts";
  import type { AskSubject } from "#lib/ask.svelte.ts";
  import AskDialog from "./AskDialog.svelte";

  interface Props {
    /** What is asked about, in a few words: "Caddy logs". */
    title: string;
    subject: AskSubject;
    small?: boolean;
  }
  let { title, subject, small = false }: Props = $props();

  // Shown only with an assistant set up (Settings, Assistant).
  const assistant = createQuery(() => ({ queryKey: keys.assistant, queryFn: fetchAssistant }));
  const on = $derived(!!assistant.data && assistant.data.provider !== "none");
  let open = $state(false);
</script>

{#if on}
  <button type="button" class="btn {small ? 'btn-sm' : ''}" onclick={() => (open = true)}>Ask</button>
  {#if open}
    <AskDialog {title} {subject} onClose={() => (open = false)} />
  {/if}
{/if}
