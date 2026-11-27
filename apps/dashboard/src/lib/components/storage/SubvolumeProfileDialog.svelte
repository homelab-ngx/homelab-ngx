<script lang="ts">
  import { untrack } from "svelte";
  import { createMutation, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import { diskCounts, effectiveCopies, folderName, isTiered, maxCopies } from "#lib/storage.ts";

  interface Props {
    pool: Schema<"Pool">;
    subvolume: Schema<"Subvolume">;
    onClose: () => void;
  }
  let { pool, subvolume, onClose }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  const name = $derived(folderName(subvolume.path, pool.mountdir));
  const tiered = $derived(isTiered(pool));
  const most = $derived(maxCopies(pool.disks.length));
  const poolCopies = $derived(Math.max(1, pool.data_replicas || 1));
  const ssdCount = $derived(diskCounts(pool).ssd);

  const startProfile = untrack(() =>
    subvolume.profile === "hot" || subvolume.profile === "cached" || subvolume.profile === "cold" ? subvolume.profile : "",
  );
  const startCopies = untrack(() => effectiveCopies(pool, subvolume));

  let profile = $state<"" | "hot" | "cached" | "cold">(startProfile);
  let copies = $state(startCopies);
  let error = $state("");

  const nextProfile = $derived(tiered && profile !== "" && profile !== subvolume.profile ? profile : undefined);
  const nextCopies = $derived(most > 1 && copies !== startCopies ? (copies === poolCopies ? 0 : copies) : undefined);
  const dirty = $derived(nextProfile !== undefined || nextCopies !== undefined);

  const save = createMutation(() => ({
    mutationFn: () => {
      const body: Schema<"SubvolumeProfileRequest"> = { path: subvolume.path };
      if (nextProfile !== undefined) body.profile = nextProfile;
      if (nextCopies !== undefined) body.copies = nextCopies;
      return call(api.PATCH("/storage/subvolume", { body }));
    },
    onMutate: () => (error = ""),
    onSuccess: onClose,
    onError: (e: unknown) => (error = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: ["daemon", "storage"] }),
  }));

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (dirty) save.mutate();
  }

  const options = [
    { value: "hot", label: "Hot", desc: "SSD only" },
    { value: "cached", label: "Cached", desc: "SSD → HDD" },
    { value: "cold", label: "Cold", desc: "HDD only" },
  ] as const;
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title="Storage profile · {name}">
  <form id="{id}-form" class="grid gap-4" onsubmit={submit}>
    {#if tiered}
      <fieldset class="grid gap-1.5">
        <legend class="mb-1 text-muted">
          <Help label="Placement">
            New writes follow the profile at once; existing files move in the background.
          </Help>
        </legend>
        {#each options as opt (opt.value)}
          <label class="flex items-baseline gap-2.5 rounded-md border border-line px-3 py-2 has-checked:border-accent">
            <input type="radio" name="{id}-profile" value={opt.value} bind:group={profile} />
            <span class="font-medium">{opt.label}</span>
            <span class="text-muted">{opt.desc}</span>
            {#if opt.value === "cached"}<span class="ml-auto text-xs text-muted">default</span>{/if}
          </label>
        {/each}
        {#if subvolume.profile === "custom" && !profile}
          <p class="text-xs text-muted">Custom bcachefs targets are set.</p>
        {/if}
        {#if profile === "hot" && copies > ssdCount}
          <p class="text-xs text-muted">Extra copies spill to HDD.</p>
        {/if}
      </fieldset>
    {/if}

    {#if most > 1}
      <fieldset class="grid gap-1.5">
        <legend class="mb-1 text-muted">
          <Help label="Copies">
            How many disks hold each file in this subvolume. Use 1 copy only for disposable data.
          </Help>
        </legend>
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
          {#each [1, 2, 3] as n (n)}
            <label class="flex items-center gap-1.5 {n > most ? 'text-muted' : ''}">
              <input type="radio" name="{id}-copies" value={n} checked={copies === n} disabled={n > most} onchange={() => (copies = n)} />
              <span>{n}</span>
              {#if n === poolCopies}<span class="text-xs text-muted">(default)</span>{/if}
            </label>
          {/each}
        </div>
        {#if copies === 1 && poolCopies > 1}
          <p class="text-xs text-warn">No disk redundancy for this subvolume.</p>
        {/if}
      </fieldset>
    {/if}

    {#if error}<p role="alert" class="text-bad">{error}</p>{/if}
  </form>
  {#snippet actions()}
    <button type="submit" form="{id}-form" class="btn btn-primary" disabled={!dirty || save.isPending}>
      {save.isPending ? "Saving…" : "Save"}
    </button>
  {/snippet}
</Modal>
