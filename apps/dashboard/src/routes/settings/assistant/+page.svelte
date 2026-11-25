<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { describe } from "#lib/api/client.ts";
  import { fetchAssistant, keys } from "#lib/api/queries.ts";
  import AssistantForm from "#lib/components/AssistantForm.svelte";
  import Help from "#lib/components/Help.svelte";

  const assistant = createQuery(() => ({ queryKey: keys.assistant, queryFn: fetchAssistant }));
</script>

<svelte:head><title>AI assistant · Settings · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/settings">Settings</a> /</p>
<h1 class="mt-1 text-xl font-semibold">
  <Help label="AI assistant">
    Helps in the stack editor. It reads the stack's two files and the last check and build output, and suggests changes. Every
    change is shown before it goes in.
  </Help>
</h1>

{#if assistant.error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(assistant.error)}</p>
{:else if assistant.data}
  <AssistantForm current={assistant.data} />
{:else}
  <p class="mt-4 text-muted">Loading…</p>
{/if}
