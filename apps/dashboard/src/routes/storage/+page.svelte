<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import { fetchStacks, keys } from "#lib/api/queries.ts";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import AddDiskDialog from "#lib/components/storage/AddDiskDialog.svelte";
  import CreatePoolDialog from "#lib/components/storage/CreatePoolDialog.svelte";
  import DeletePoolDialog from "#lib/components/storage/DeletePoolDialog.svelte";
  import PartitionsBox from "#lib/components/storage/PartitionsBox.svelte";
  import PoolBox from "#lib/components/storage/PoolBox.svelte";
  import RemoveDiskDialog from "#lib/components/storage/RemoveDiskDialog.svelte";
  import SpareBox from "#lib/components/storage/SpareBox.svelte";
  import SubvolumesBox from "#lib/components/storage/SubvolumesBox.svelte";
  import Switch from "#lib/components/Switch.svelte";
  import SystemDisk from "#lib/components/SystemDisk.svelte";
  import { stackTitles } from "#lib/stacks.ts";
  import { disksSummary, folderGroups, foldersOf, running, usedBy } from "#lib/storage.ts";
  import { setAdvanced, storageView } from "#lib/storageView.svelte.ts";

  type Pool = Schema<"Pool">;
  type Disk = Schema<"Disk">;
  type Partition = Schema<"Partition">;
  type StorageStatus = Schema<"StorageStatus">;

  const key = ["daemon", "storage"] as const;
  const client = useQueryClient();
  const storage = createQuery(() => ({
    queryKey: key,
    queryFn: () => call<StorageStatus>(api.GET("/storage")),
    // A disk that is leaving a pool is followed closely.
    refetchInterval: (q) => (q.state.data?.pools.some((p) => running(p.operation)) ? 2_000 : 10_000),
  }));
  const stacks = createQuery(() => ({ queryKey: keys.stacks, queryFn: fetchStacks }));
  const title = $derived(stackTitles(stacks.data));

  const pools = $derived(storage.data?.pools ?? []);
  const subvolumes = $derived(storage.data?.subvolumes ?? []);
  const spare = $derived(storage.data?.unassigned ?? []);
  // A partition is a file system that is not a pool: the mounted ones are listed under the
  // subvolumes, the ones that can be mounted last, with the spare disks.
  const partitions = $derived(storage.data?.partitions ?? []);
  const mountedParts = $derived(partitions.filter((p) => p.state === "mounted"));
  const spareParts = $derived(partitions.filter((p) => p.state !== "mounted"));
  const allDisks = $derived([...pools.flatMap((p) => p.disks), ...spare]);
  const disks = $derived(disksSummary(allDisks));
  const groups = $derived(folderGroups(pools, subvolumes));
  const advanced = $derived(storageView.advanced);

  const status = $derived.by(() => {
    if (storage.isPending) return "Reading the disks…";
    const where = pools.length === 1 ? pools[0]!.mountdir || pools[0]!.name : `${pools.length} pools`;
    const n = allDisks.length;
    return [where, `${n} disk${n === 1 ? "" : "s"}`, disks.text].filter(Boolean).join(" · ");
  });

  let error = $state("");
  let confirm = $state<{ title: string; body: string; label: string; run: () => Promise<unknown> } | null>(null);
  let newSubvolume = $state<Pool | null>(null);
  /** The open pool dialog. Each one runs its own request. */
  let dialog = $state<
    | { kind: "create" }
    | { kind: "add"; pool: Pool }
    | { kind: "remove"; pool: Pool; disk: Disk }
    | { kind: "delete"; pool: Pool }
    | null
  >(null);
  /** The names taken under /var/mnt. */
  const taken = $derived([
    ...pools.filter((p) => p.state === "mounted" || p.auto_mount).map((p) => p.mount_name),
    ...mountedParts.map((p) => p.mount_name),
  ]);
  /** Stops a removal, or clears one that ended. */
  const endRemoval = (p: Pool) =>
    op.mutate(() => call(api.DELETE("/storage/pools/{uuid}/operation", { params: { path: { uuid: p.uuid } } })));
  let name = $state("");

  /** Runs one storage action; every action reads the pools again. */
  const op = createMutation(() => ({
    mutationFn: (run: () => Promise<unknown>) => run(),
    onMutate: () => (error = ""),
    onSuccess: () => {
      confirm = null;
      newSubvolume = null;
      name = "";
    },
    onError: (e: unknown) => (error = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: key }),
  }));
  const measure = createMutation(() => ({
    mutationFn: (paths: string[]) => call(api.POST("/storage/subvolume-usage", { body: { paths } })),
    onMutate: () => (error = ""),
    onError: (e: unknown) => (error = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: key }),
  }));

  /** The pool or partition whose Mount is running, by uuid: its button says so. */
  let mounting = $state("");
  /** Mounts a pool under /var/mnt; no name is the pool's own (mount_name). */
  const mount = (p: Pool, as: string) => {
    mounting = p.uuid;
    op.mutate(() => call(api.POST("/storage/mount", { body: { devices: p.disks.map((d) => d.path), name: as || undefined } })));
  };
  const mountPartition = (p: Partition, as: string, readOnly: boolean) => {
    mounting = p.uuid;
    op.mutate(() =>
      call(
        api.POST("/storage/partitions/{uuid}/mount", {
          params: { path: { uuid: p.uuid } },
          body: { name: as || undefined, read_only: readOnly || undefined },
        }),
      ),
    );
  };
  const partitionAutoMount = (p: Partition, on: boolean) =>
    op.mutate(() => call(api.PATCH("/storage/partitions/{uuid}", { params: { path: { uuid: p.uuid } }, body: { auto_mount: on } })));
  const askUnmountPartition = (p: Partition) =>
    (confirm = {
      title: `Unmount ${p.mountdir}?`,
      body: `Stops only if nothing uses ${p.mountdir}: stop its stacks first.`,
      label: "Unmount",
      run: () => call(api.POST("/storage/partitions/{uuid}/unmount", { params: { path: { uuid: p.uuid } } })),
    });
  const autoMount = (p: Pool, on: boolean) =>
    op.mutate(() => call(api.PATCH("/storage/pools/{uuid}", { params: { path: { uuid: p.uuid } }, body: { auto_mount: on } })));
  const askUnmount = (p: Pool) =>
    (confirm = {
      title: `Unmount ${p.mountdir}?`,
      body: `Stops only if nothing uses ${p.mountdir}: stop its stacks and services first.`,
      label: "Unmount",
      run: () => call(api.POST("/storage/unmount", { body: { mountpoint: p.mountdir } })),
    });
  const askDelete = (path: string) =>
    (confirm = {
      title: `Delete ${path}?`,
      body: "Only an empty subvolume can be deleted.",
      label: "Delete subvolume",
      run: () => call(api.DELETE("/storage/subvolume", { params: { query: { path } } })),
    });
  function create(e: SubmitEvent) {
    e.preventDefault();
    const p = newSubvolume;
    if (!p) return;
    const path = `${p.mountdir}/${name.trim().replace(/^\/+/, "")}`;
    op.mutate(() => call(api.POST("/storage/subvolume", { body: { path } })));
  }

  const toneText = { ok: "text-muted", warn: "text-warn", bad: "text-bad", info: "text-muted", idle: "text-muted" } as const;
</script>

<svelte:head><title>Storage · Homelab-ngx</title></svelte:head>

<div class="flex flex-wrap items-start justify-between gap-3">
  <div>
    <h1 class="text-xl font-semibold">Storage</h1>
    <p class="mt-0.5 {toneText[disks.tone]}">{status}</p>
  </div>
  <span class="flex items-center gap-2 pt-1">
    <span aria-hidden="true">Advanced</span>
    <Switch checked={advanced} label="Advanced" onChange={setAdvanced} />
  </span>
</div>

{#if error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
{/if}
{#if storage.error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(storage.error)}</p>
{/if}

<div class="mt-5 grid gap-5">
  {#if storage.data && pools.length === 0}
    <div class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-line bg-surface px-4 py-3">
      <p class="text-muted">No storage pools found. <a class="link" href="/storage/browse">Browse stack folders</a></p>
      {#if spare.length > 0}
        <button type="button" class="btn btn-primary" onclick={() => (dialog = { kind: "create" })}>Create pool…</button>
      {/if}
    </div>
  {/if}

  {#each pools as p (p.uuid)}
    <PoolBox
      pool={p}
      folders={foldersOf(p, subvolumes)}
      {advanced}
      busy={op.isPending}
      mounting={mounting === p.uuid}
      onMount={(as) => mount(p, as)}
      onAutoMount={(on) => autoMount(p, on)}
      onAddDisk={() => (dialog = { kind: "add", pool: p })}
      onRemoveDisk={(d) => (dialog = { kind: "remove", pool: p, disk: d })}
      onUnmount={() => askUnmount(p)}
      onDelete={() => (dialog = { kind: "delete", pool: p })}
      onEndRemoval={() => endRemoval(p)}
    />
  {/each}

  {#if groups.length > 0}
    <SubvolumesBox
      {groups}
      stacks={stacks.data ?? []}
      busy={op.isPending}
      measuring={measure.isPending}
      onMeasure={() => measure.mutate(groups.flatMap((g) => g.folders.map((f) => f.path)))}
      onNew={(p) => (newSubvolume = p)}
      onDelete={askDelete}
    />
  {/if}

  {#if advanced && mountedParts.length > 0}
    <PartitionsBox partitions={mountedParts} stacks={stacks.data ?? []} busy={op.isPending} onAutoMount={partitionAutoMount} onUnmount={askUnmountPartition} />
  {/if}

  {#if advanced && storage.data}
    <SpareBox
      disks={spare}
      partitions={spareParts}
      busy={op.isPending}
      {mounting}
      onCreatePool={pools.length > 0 ? () => (dialog = { kind: "create" }) : undefined}
      onMount={mountPartition}
    />
  {/if}

  <SystemDisk />
</div>

{#if newSubvolume}
  {@const p = newSubvolume}
  <Modal
    open={true}
    onOpenChange={(o) => !o && (newSubvolume = null)}
    title="New subvolume on {p.mountdir}"
  >
    <form id="new-subvolume" class="grid gap-1.5" onsubmit={create}>
      <div class="text-muted">
        <Help label="Name">
          A folder the pool keeps as its own unit, with its own size and snapshots. Moving files between subvolumes copies them, and
          hard links cannot cross: keep downloads and the library they link to in one.
        </Help>
      </div>
      <div class="flex items-center gap-1.5">
        <span class="text-muted">{p.mountdir}/</span>
        <input id="subvolume-name" class="input min-w-0 flex-1" autocomplete="off" aria-label="Subvolume name" bind:value={name} />
      </div>
      {#if error}<p role="alert" class="text-bad">{error}</p>{/if}
    </form>
    {#snippet actions()}
      <button type="submit" form="new-subvolume" class="btn btn-primary" disabled={op.isPending || !name.trim()}>Create</button>
    {/snippet}
  </Modal>
{/if}

{#if dialog?.kind === "create"}
  <CreatePoolDialog {spare} {taken} onClose={() => (dialog = null)} />
{:else if dialog?.kind === "add"}
  <AddDiskDialog pool={dialog.pool} {spare} onClose={() => (dialog = null)} />
{:else if dialog?.kind === "remove"}
  <RemoveDiskDialog pool={dialog.pool} disk={dialog.disk} onClose={() => (dialog = null)} />
{:else if dialog?.kind === "delete"}
  <DeletePoolDialog pool={dialog.pool} users={usedBy(dialog.pool.mountdir, stacks.data ?? []).map(title)} onClose={() => (dialog = null)} />
{/if}

{#if confirm}
  {@const c = confirm}
  <Modal open={true} onOpenChange={(o) => !o && (confirm = null)} title={c.title} description={c.body}>
    {#if error}<p class="text-bad">{error}</p>{/if}
    {#snippet actions()}
      <button type="button" class="btn btn-danger" disabled={op.isPending} onclick={() => op.mutate(c.run)}>{c.label}</button>
    {/snippet}
  </Modal>
{/if}
