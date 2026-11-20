<script lang="ts">
  import { createMutation, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import { maxCopies, tiering, withContents } from "#lib/storage.ts";
  import DiskLine from "./DiskLine.svelte";

  interface Props {
    /** The disks a pool can be made from. */
    spare: Schema<"Disk">[];
    /** The names pools and partitions already mount under. */
    taken: string[];
    onClose: () => void;
  }
  let { spare, taken, onClose }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  let name = $state("");
  const ticked = $state<Record<string, boolean>>({});
  let copies = $state(1);
  let typed = $state("");
  let error = $state("");

  const picked = $derived(spare.filter((d) => ticked[d.path]));
  const most = $derived(maxCopies(picked.length));
  const kept = $derived(Math.min(copies, most));
  const erased = $derived(withContents(picked));
  const tiers = $derived(tiering(picked));
  const blocked = $derived.by(() => {
    if (!/^[a-z0-9][a-z0-9-]{0,30}$/.test(name)) return name ? "A name has lowercase letters, digits and dashes." : "";
    if (taken.includes(name)) return `/var/mnt/${name} is taken.`;
    return "";
  });
  const ready = $derived(!!name && !blocked && picked.length > 0 && (erased.length === 0 || typed === name));

  const create = createMutation(() => ({
    mutationFn: () =>
      call(
        api.POST("/storage/pools", {
          body: { name, disks: picked.map((d) => d.path), copies: kept, confirm: erased.length > 0 ? typed : undefined },
        }),
      ),
    onMutate: () => (error = ""),
    onSuccess: onClose,
    onError: (e: unknown) => (error = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: ["daemon", "storage"] }),
  }));

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (ready) create.mutate();
  }
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title="Create pool">
  <form id="{id}-form" class="grid gap-4" onsubmit={submit}>
    <div class="grid gap-1">
      <label for="{id}-name" class="text-muted">Name</label>
      <div class="flex items-center gap-1.5">
        <span class="text-muted">/var/mnt/</span>
        <input id="{id}-name" class="input min-w-0 flex-1" autocomplete="off" spellcheck="false" maxlength="31" bind:value={name} />
      </div>
    </div>

    <fieldset class="grid gap-1">
      <legend class="mb-1 text-muted">Disks</legend>
      {#each spare as d (d.path)}
        <label class="flex items-baseline gap-2.5 rounded-md border border-line px-3 py-2 has-checked:border-accent">
          <input type="checkbox" bind:checked={ticked[d.path]} />
          <DiskLine disk={d} />
        </label>
      {/each}
      {#if tiers}<p class="text-xs text-muted">{tiers}</p>{/if}
    </fieldset>

    <fieldset class="flex flex-wrap items-center gap-x-4 gap-y-1">
      <legend class="mb-1 text-muted">
        <Help label="Copies">
          Each copy of a file is on a different disk. With 2 copies one disk can fail without losing files, and the pool holds
          half as much.
        </Help>
      </legend>
      {#each [1, 2, 3] as n (n)}
        <label class="flex items-center gap-1.5 {n > most ? 'text-muted' : ''}">
          <input type="radio" name="{id}-copies" value={n} checked={kept === n} disabled={n > most} onchange={() => (copies = n)} />
          {n}
        </label>
      {/each}
    </fieldset>

    {#if erased.length > 0}
      <div class="grid gap-1">
        <p class="text-warn">Everything on {erased.map((d) => d.name).join(", ")} is erased.</p>
        <label for="{id}-typed" class="text-muted">Type {name || "the name"} again</label>
        <input id="{id}-typed" class="input" autocomplete="off" spellcheck="false" bind:value={typed} />
      </div>
    {/if}
    {#if blocked}<p class="text-muted">{blocked}</p>{/if}
    {#if error}<p role="alert" class="text-bad">{error}</p>{/if}
  </form>
  {#snippet actions()}
    <button type="submit" form="{id}-form" class="btn btn-primary" title={blocked} disabled={!ready || create.isPending}>
      {create.isPending ? "Creating…" : "Create pool"}
    </button>
  {/snippet}
</Modal>
