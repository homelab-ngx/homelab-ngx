<script lang="ts">
  import { useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Stack } from "#lib/api/client.ts";
  import { keys } from "#lib/api/queries.ts";
  import Modal from "#lib/components/Modal.svelte";
  import { stackTitle } from "#lib/stacks.ts";

  interface Props {
    stack: Stack;
    onClose: () => void;
  }
  let { stack, onClose }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  let ageKey = $state("");
  let fileName = $state("");
  let error = $state("");
  let pending = $state(false);

  async function pickFile(e: Event) {
    const file = (e.currentTarget as HTMLInputElement).files?.[0];
    if (!file) return;
    error = "";
    try {
      ageKey = await file.text();
      fileName = file.name;
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!ageKey.trim() || pending) return;
    pending = true;
    error = "";
    try {
      const s = await call<Stack>(
        api.POST("/stacks/{name}/variables/reseal", {
          params: { path: { name: stack.name } },
          body: { age_key: ageKey.trim() },
        }),
      );
      client.setQueryData([...keys.stacks, stack.name], s);
      void client.invalidateQueries({ queryKey: keys.stacks });
      onClose();
    } catch (err) {
      error = describe(err);
    } finally {
      pending = false;
    }
  }
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title="Unlock {stackTitle(stack)} variables">
  <form id="{id}-form" class="grid gap-3" onsubmit={submit}>
    <p class="text-muted">Provide the previous host's keys.txt to re-seal variables to this host.</p>
    <div class="grid gap-1">
      <label for="{id}-file" class="text-muted">Age key file (keys.txt)</label>
      <input id="{id}-file" type="file" accept=".txt,text/plain" onchange={pickFile} />
      {#if fileName}<p class="text-xs text-muted">Loaded {fileName}.</p>{/if}
    </div>
    <div class="grid gap-1">
      <label for="{id}-key" class="text-muted">Or paste age key</label>
      <textarea
        id="{id}-key"
        class="input min-h-24"
        rows="4"
        autocomplete="off"
        spellcheck={false}
        placeholder="AGE-SECRET-KEY-1..."
        bind:value={ageKey}
      ></textarea>
    </div>
    {#if error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
  </form>
  {#snippet actions()}
    <button type="submit" form="{id}-form" class="btn btn-primary" disabled={pending || !ageKey.trim()}>
      {pending ? "Unlocking…" : "Unlock"}
    </button>
  {/snippet}
</Modal>
