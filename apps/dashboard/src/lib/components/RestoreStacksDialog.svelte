<script lang="ts">
  import { useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import { keys } from "#lib/api/queries.ts";
  import Modal from "#lib/components/Modal.svelte";

  type StackRestore = Schema<"StackRestore">;
  type StackRestoreResult = Schema<"StackRestoreResult">;

  interface Props {
    onClose: () => void;
  }
  let { onClose }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  let lock = $state<{ version: number; stacks: NonNullable<StackRestore["stacks"]> } | null>(null);
  let lockFileName = $state("");
  let lockError = $state("");
  let ageKey = $state("");
  let keyFileName = $state("");
  let submitError = $state("");
  let pending = $state(false);
  let result = $state<StackRestoreResult | null>(null);

  const skippedEntries = $derived(result ? Object.entries(result.skipped) : []);
  const resealedList = $derived(result?.resealed ?? []);
  const shownError = $derived(lockError || submitError);

  async function pickLockFile(e: Event) {
    const file = (e.currentTarget as HTMLInputElement).files?.[0];
    if (!file) return;
    lockError = "";
    submitError = "";
    try {
      const parsed = JSON.parse(await file.text()) as { version?: unknown; stacks?: unknown };
      if (typeof parsed?.version !== "number" || !Array.isArray(parsed?.stacks)) {
        lock = null;
        lockFileName = "";
        lockError = "Choose a valid stacks.lock.json file.";
        return;
      }
      lock = { version: parsed.version, stacks: parsed.stacks as NonNullable<StackRestore["stacks"]> };
      lockFileName = file.name;
    } catch {
      lock = null;
      lockFileName = "";
      lockError = "Could not read lock file JSON.";
    }
  }

  function clearLockFile() {
    lock = null;
    lockFileName = "";
    lockError = "";
  }

  async function pickKeyFile(e: Event) {
    const file = (e.currentTarget as HTMLInputElement).files?.[0];
    if (!file) return;
    submitError = "";
    try {
      ageKey = await file.text();
      keyFileName = file.name;
    } catch (err) {
      submitError = err instanceof Error ? err.message : String(err);
    }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (pending || lockError) return;
    pending = true;
    submitError = "";
    try {
      const body: StackRestore = lock ? { version: lock.version, stacks: lock.stacks } : { use_host_lock: true };
      const trimmed = ageKey.trim();
      if (trimmed) body.age_key = trimmed;
      result = await call<StackRestoreResult>(api.POST("/stacks-lock/restore", { body }));
      void client.invalidateQueries({ queryKey: keys.stacks });
      void client.invalidateQueries({ queryKey: keys.services });
    } catch (err) {
      submitError = describe(err);
    } finally {
      pending = false;
    }
  }
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title="Restore stacks">
  {#if result}
    <div class="grid gap-3">
      {#if result.restored.length === 0 && resealedList.length === 0 && skippedEntries.length === 0}
        <p class="text-muted">Nothing to restore.</p>
      {/if}
      {#if result.restored.length}
        <div>
          <p class="label mb-1">Restored</p>
          <ul class="list-disc pl-5">
            {#each result.restored as name (name)}<li>{name}</li>{/each}
          </ul>
        </div>
      {/if}
      {#if resealedList.length}
        <div>
          <p class="label mb-1">Re-sealed variables</p>
          <ul class="list-disc pl-5">
            {#each resealedList as name (name)}<li>{name}</li>{/each}
          </ul>
        </div>
      {/if}
      {#if skippedEntries.length}
        <div>
          <p class="label mb-1 text-warn">Skipped</p>
          <ul class="space-y-1 text-warn">
            {#each skippedEntries as [name, why] (name)}<li>{name}: {why}</li>{/each}
          </ul>
        </div>
      {/if}
    </div>
  {:else}
    <form id="{id}-form" class="grid gap-3" onsubmit={submit}>
      <div class="grid gap-1">
        <div class="flex items-center justify-between gap-2">
          <label for="{id}-lock" class="text-muted">Lock file (optional)</label>
          {#if lock}
            <button type="button" class="text-xs underline" onclick={clearLockFile}>Use host lock</button>
          {/if}
        </div>
        <input id="{id}-lock" type="file" accept=".json,application/json" onchange={pickLockFile} />
        <p class="text-xs text-muted">
          {lock ? `${lockFileName} (${lock.stacks.length} stacks)` : "Default: host stacks.lock.json"}
        </p>
      </div>
      <div class="grid gap-1">
        <label for="{id}-key-file" class="text-muted">Previous host age key file (optional)</label>
        <input id="{id}-key-file" type="file" accept=".txt,text/plain" onchange={pickKeyFile} />
        {#if keyFileName}<p class="text-xs text-muted">Loaded {keyFileName}.</p>{/if}
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
      {#if shownError}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{shownError}</p>{/if}
    </form>
  {/if}
  {#snippet actions()}
    {#if result}
      <button type="button" class="btn btn-primary" onclick={onClose}>Done</button>
    {:else}
      <button type="submit" form="{id}-form" class="btn btn-primary" disabled={pending || !!lockError}>
        {pending ? "Restoring…" : "Restore"}
      </button>
    {/if}
  {/snippet}
</Modal>
