<script lang="ts">
  import { ChevronDown, LoaderCircle } from "@lucide/svelte";
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { DropdownMenu } from "bits-ui";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import { fetchStacks, keys } from "#lib/api/queries.ts";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import SystemDisk from "#lib/components/SystemDisk.svelte";
  import { ago, bytes } from "#lib/format.ts";
  import { stackTitles } from "#lib/stacks.ts";
  import { browseHref, capacity, diskState, disksSummary, folderName, foldersOf, fullness, measuredAt, role, segments, usedBy } from "#lib/storage.ts";

  type Pool = Schema<"Pool">;
  type Disk = Schema<"Disk">;
  type Partition = Schema<"Partition">;
  type StorageStatus = Schema<"StorageStatus">;

  const key = ["daemon", "storage"] as const;
  const client = useQueryClient();
  const storage = createQuery(() => ({
    queryKey: key,
    queryFn: () => call<StorageStatus>(api.GET("/storage")),
    refetchInterval: 10_000,
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

  const status = $derived.by(() => {
    if (storage.isPending) return "Reading the disks…";
    const where = pools.length === 1 ? pools[0]!.mountdir || pools[0]!.name : `${pools.length} pools`;
    const n = allDisks.length;
    return [where, `${n} disk${n === 1 ? "" : "s"}`, disks.text].filter(Boolean).join(" · ");
  });

  let error = $state("");
  let confirm = $state<{ title: string; body: string; label: string; run: () => Promise<unknown> } | null>(null);
  let newSubvolume = $state<Pool | null>(null);
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

  /** The name typed for each unmounted pool, by uuid; none typed is the pool's own (mount_name). */
  const mountNames = $state<Record<string, string>>({});
  /** The pool whose Mount is running: its button says so. */
  let mounting = $state("");
  const mount = (p: Pool) => {
    mounting = p.uuid;
    op.mutate(() =>
      call(
        api.POST("/storage/mount", {
          body: { devices: p.disks.map((d) => d.path), name: mountNames[p.uuid]?.trim() || undefined },
        }),
      ),
    );
  };
  /** What is typed and ticked for each partition that can be mounted, by uuid. */
  const partNames = $state<Record<string, string>>({});
  const partReadOnly = $state<Record<string, boolean>>({});
  const mountPartition = (p: Partition) => {
    mounting = p.uuid;
    op.mutate(() =>
      call(
        api.POST("/storage/partitions/{uuid}/mount", {
          params: { path: { uuid: p.uuid } },
          body: { name: partNames[p.uuid]?.trim() || undefined, read_only: partReadOnly[p.uuid] || undefined },
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

  const roleWord = { cache: "Cache", storage: "Storage", "": "–" } as const;
  const shade = (i: number, other?: boolean) =>
    other ? "var(--muted)" : `var(--series-${i + 1})`;
  const toneText = { ok: "text-muted", warn: "text-warn", bad: "text-bad", info: "text-muted", idle: "text-muted" } as const;
</script>

<svelte:head><title>Storage · Homelab-ngx</title></svelte:head>

{#snippet manage(p: Pool)}
  <DropdownMenu.Root>
    <DropdownMenu.Trigger class="btn" disabled={op.isPending}>
      Manage pool <ChevronDown class="size-3.5" aria-hidden="true" />
    </DropdownMenu.Trigger>
    <DropdownMenu.Portal>
      <DropdownMenu.Content
        align="end"
        sideOffset={6}
        class="z-50 min-w-60 rounded-md border border-line bg-surface p-1 shadow-lg"
      >
        <DropdownMenu.CheckboxItem
          checked={p.auto_mount}
          onCheckedChange={(on) => autoMount(p, on)}
          class="flex cursor-default items-center justify-between gap-4 rounded px-2.5 py-1.5 outline-none data-highlighted:bg-sunken"
        >
          {#snippet children({ checked })}
            Mount at startup
            <span
              aria-hidden="true"
              class="relative h-4 w-7 shrink-0 rounded-full transition-colors {checked ? 'bg-accent' : 'bg-line'}"
            >
              <span class="absolute top-0.5 size-3 rounded-full bg-surface transition-[left] {checked ? 'left-3.5' : 'left-0.5'}"></span>
            </span>
          {/snippet}
        </DropdownMenu.CheckboxItem>
        {#if p.state === "mounted"}
          <DropdownMenu.Item
            onSelect={() => (newSubvolume = p)}
            class="cursor-default rounded px-2.5 py-1.5 outline-none data-highlighted:bg-sunken"
          >
            New subvolume…
          </DropdownMenu.Item>
          <DropdownMenu.Separator class="my-1 border-t border-line" />
          <DropdownMenu.Item
            onSelect={() => askUnmount(p)}
            class="cursor-default rounded px-2.5 py-1.5 text-bad outline-none data-highlighted:bg-bad-soft"
          >
            Unmount…
          </DropdownMenu.Item>
        {/if}
      </DropdownMenu.Content>
    </DropdownMenu.Portal>
  </DropdownMenu.Root>
{/snippet}

<div class="flex flex-wrap items-end justify-between gap-3">
  <div>
    <h1 class="text-xl font-semibold">Storage</h1>
    <p class="mt-0.5 {toneText[disks.tone]}">{status}</p>
  </div>
  {#if pools.length === 1}
    {@render manage(pools[0]!)}
  {/if}
</div>

{#if error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
{/if}
{#if storage.error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(storage.error)}</p>
{/if}

{#if storage.data && pools.length === 0}
  <p class="mt-5 rounded-lg border border-line bg-surface px-4 py-3 text-muted">
    No storage pools found. <a class="link" href="/storage/browse">Browse stack folders</a>
  </p>
{/if}

{#each pools as p (p.uuid)}
  {@const cap = capacity(p)}
  {@const folders = foldersOf(p, subvolumes)}
  {@const segs = cap ? segments(folders, cap.total, p.mountdir, cap.used) : []}
  {@const measured = measuredAt(folders)}
  <section class="mt-5" aria-label={p.mountdir || p.name}>
    {#if pools.length > 1}
      <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
        <h2 class="font-semibold">{p.mountdir || p.name}</h2>
        {@render manage(p)}
      </div>
    {/if}

    <div class="card">
      {#if p.state !== "mounted"}
        <form
          class="flex flex-wrap items-center gap-3"
          onsubmit={(e) => {
            e.preventDefault();
            mount(p);
          }}
        >
          <Pill tone="idle">unmounted</Pill>
          <label class="ml-auto flex min-w-0 items-center gap-2">
            <span class="text-muted">/var/mnt/</span>
            <input
              class="input w-40 min-w-0"
              aria-label="Name"
              autocomplete="off"
              spellcheck="false"
              pattern="[a-z0-9][a-z0-9-]*"
              maxlength="31"
              placeholder={p.mount_name}
              bind:value={mountNames[p.uuid]}
            />
          </label>
          <button type="submit" class="btn btn-primary" disabled={op.isPending}>
            {#if mounting === p.uuid && op.isPending}<LoaderCircle class="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />Mounting…{:else}Mount{/if}
          </button>
        </form>
      {:else if cap}
        <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 tabular-nums">
          <p>
            <span class="text-lg">{bytes(cap.used)}</span>
            <span class="text-muted">used of {bytes(cap.total)}</span>
          </p>
          <p class={toneText[fullness(cap.percent)]}>
            {bytes(cap.free)} free
          </p>
        </div>
        <div
          class="mt-2 flex h-2 overflow-hidden rounded-full bg-sunken"
          role="img"
          aria-label="{Math.round(cap.percent)}% used"
        >
          {#if segs.length > 0}
            {#each segs as s, i (s.label)}
              <span class="h-full not-first:border-l not-first:border-surface" style:width="{s.percent}%" style:background={shade(i, s.other)}></span>
            {/each}
          {:else}
            <span class="h-full bg-accent" style:width="{cap.percent}%"></span>
          {/if}
        </div>
        <div class="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted">
          {#each segs as s, i (s.label)}
            <span class="inline-flex items-center gap-1.5">
              <span aria-hidden="true" class="size-2 rounded-sm" style:background={shade(i, s.other)}></span>
              <span>{s.label}</span>
              <span class="tabular-nums">{bytes(s.bytes)}</span>
            </span>
          {/each}
          <Help label={p.data_replicas > 1 ? `${p.data_replicas} copies of each file` : "1 copy of each file"} class="sm:ml-auto">
            {#if p.data_replicas > 1}
              Each copy is on a different disk, so one disk can fail without losing files. Sizes on this page count one copy.
            {:else}
              If a disk fails, the files on it are gone and only backups still have them.
            {/if}
          </Help>
        </div>
      {:else}
        <p class="text-muted">The pool's size could not be read.</p>
      {/if}
    </div>

    {#if p.state === "mounted"}
      <div class="mt-6 mb-2 flex flex-wrap items-center justify-between gap-2">
        <h2 class="min-w-0 font-semibold">
          <Help label="Subvolumes">
            Folders where stacks and backups keep files. Sizes are recalculated every few hours, before compression; data that
            files share is counted once.
          </Help>
        </h2>
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-xs text-muted">
            {measure.isPending ? "Recalculating…" : measured ? `Calculated ${ago(measured)}` : "Not calculated"}
          </span>
          <button
            type="button"
            class="btn btn-sm"
            disabled={measure.isPending || folders.length === 0}
            onclick={() => measure.mutate(folders.map((f) => f.path))}
          >
            {#if measure.isPending}
              <LoaderCircle class="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
            {/if}
            Recalculate sizes
          </button>
        </div>
      </div>
      <div class="relative overflow-x-auto rounded-lg border border-line bg-surface">
        <table class="w-full border-collapse text-left tabular-nums">
          <thead>
            <tr class="border-b border-line bg-sunken">
              <th scope="col" class="label px-3 py-2">Subvolume</th>
              <th scope="col" class="label px-3 py-2 text-right">Size</th>
              <th scope="col" class="label px-3 py-2 whitespace-nowrap max-sm:hidden">Used by</th>
              <th scope="col" class="px-3 py-2"><span class="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {#each folders as f (f.id)}
              {@const users = usedBy(f.path, stacks.data ?? [])}
              <tr class="border-b border-line last:border-0">
                <td class="px-3 py-2">
                  <a class="link [overflow-wrap:anywhere]" href={browseHref(f.path)} title={f.path}>{folderName(f.path, p.mountdir)}</a>
                  {#if users.length > 0}
                    <span class="mt-1 flex flex-wrap gap-1 sm:hidden">{@render chips(users)}</span>
                  {/if}
                </td>
                <td class="px-3 py-2 text-right whitespace-nowrap">{f.used_bytes < 0 ? "–" : bytes(f.used_bytes)}</td>
                <td class="px-3 py-2 max-sm:hidden">
                  {#if users.length > 0}
                    <span class="flex flex-wrap gap-1">{@render chips(users)}</span>
                  {:else}
                    <span class="text-muted">–</span>
                  {/if}
                </td>
                <td class="px-3 py-2 text-right">
                  <RowMenu
                    name={f.path}
                    items={[{ label: users.length > 0 ? "Delete… (in use)" : "Delete…", danger: true, disabled: op.isPending || users.length > 0, onSelect: () => askDelete(f.path) }]}
                  />
                </td>
              </tr>
            {:else}
              <tr><td colspan="4" class="px-3 py-3 text-muted">No subvolumes yet.</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}

    {#if pools.length === 1}{@render partitionTable()}{/if}

    <h2 class="mt-6 mb-2 font-semibold">
      <Help label="Disks">
        {#if p.background_target}New files land on the cache disks and move to the storage disks later.{/if}
        Bad sectors are spots that stopped reading reliably. A few can be harmless; a growing number means the disk is wearing
        out.
      </Help>
    </h2>
    {@render diskTable(p.disks, p)}
  </section>
{/each}

{#if pools.length !== 1}{@render partitionTable()}{/if}

{#snippet partitionTable()}
  {#if mountedParts.length > 0}
    <h2 class="mt-6 mb-2 font-semibold">
      <Help label="Partitions">Data on a partition is on one disk and has no parity. A pool keeps copies on other disks.</Help>
    </h2>
    <div class="relative overflow-x-auto rounded-lg border border-line bg-surface">
      <table class="w-full border-collapse text-left tabular-nums">
        <thead>
          <tr class="border-b border-line bg-sunken">
            <th scope="col" class="label px-3 py-2">Partition</th>
            <th scope="col" class="label px-3 py-2 text-right">Used</th>
            <th scope="col" class="label px-3 py-2 whitespace-nowrap max-sm:hidden">Used by</th>
            <th scope="col" class="px-3 py-2"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each mountedParts as part (part.uuid)}
            {@const users = usedBy(part.mountdir, stacks.data ?? [])}
            <tr class="border-b border-line last:border-0">
              <td class="px-3 py-2">
                <span class="flex flex-wrap items-center gap-x-3 gap-y-1">
                  <a class="link [overflow-wrap:anywhere]" href={browseHref(part.mountdir)} title="{part.mountdir} · {part.path} · {part.fstype}">{part.mount_name}</a>
                  <Pill tone="warn">no parity</Pill>
                  {#if part.read_only}<Pill tone="idle">read-only</Pill>{/if}
                </span>
                {#if users.length > 0}
                  <span class="mt-1 flex flex-wrap gap-1 sm:hidden">{@render chips(users)}</span>
                {/if}
              </td>
              <td class="px-3 py-2 text-right whitespace-nowrap">
                {#if part.usage}
                  {bytes(part.usage.total - part.usage.free)} <span class="text-muted">of {bytes(part.usage.total)}</span>
                {:else}
                  {bytes(part.size)}
                {/if}
              </td>
              <td class="px-3 py-2 max-sm:hidden">
                {#if users.length > 0}
                  <span class="flex flex-wrap gap-1">{@render chips(users)}</span>
                {:else}
                  <span class="text-muted">–</span>
                {/if}
              </td>
              <td class="px-3 py-2 text-right">
                <RowMenu
                  name={part.mount_name}
                  items={[
                    { label: part.auto_mount ? "Do not mount at start" : "Mount at start", disabled: op.isPending, onSelect: () => partitionAutoMount(part, !part.auto_mount) },
                    { label: users.length > 0 ? "Unmount… (in use)" : "Unmount…", danger: true, disabled: op.isPending || users.length > 0, onSelect: () => askUnmountPartition(part) },
                  ]}
                />
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/snippet}

{#snippet chips(users: string[])}
  {#each users as u (u)}
    <a class="rounded border border-line bg-sunken px-1.5 text-xs hover:border-accent hover:text-accent" href="/stacks/{u}">{title(u)}</a>
  {/each}
{/snippet}

{#snippet diskTable(list: Disk[], p?: Pool)}
  <div class="relative overflow-x-auto rounded-lg border border-line bg-surface">
    <table class="w-full border-collapse text-left tabular-nums">
      <thead>
        <tr class="border-b border-line bg-sunken">
          <th scope="col" class="label px-3 py-2">Disk</th>
          {#if p}
<th scope="col" class="label px-3 py-2 max-md:hidden">Role</th>
          {/if}
          <th scope="col" class="label px-3 py-2 text-right">Size</th>
          <th scope="col" class="label px-3 py-2">Health</th>
          <th scope="col" class="label px-3 py-2 text-right max-md:hidden">Temp</th>
          <th scope="col" class="label px-3 py-2 text-right max-md:hidden">Hours on</th>
          <th scope="col" class="label px-3 py-2 text-right max-md:hidden">Bad sectors</th>
        </tr>
      </thead>
      <tbody>
        {#each list as d (d.path)}
          {@const st = diskState(d)}
          {@const h = d.health}
          <tr class="border-b border-line last:border-0">
            <td class="px-3 py-2">
              <span>{d.name}</span>
              <span class="ml-1.5 text-muted max-sm:block max-sm:ml-0 max-sm:text-xs">{d.friendly_name || d.model || ""}</span>
            </td>
            {#if p}
              <td class="px-3 py-2 whitespace-nowrap max-md:hidden">{roleWord[role(p, d)]}</td>
            {/if}
            <td class="px-3 py-2 text-right whitespace-nowrap">{bytes(d.size)}</td>
            <td class="px-3 py-2"><Pill tone={st.tone}>{st.word}</Pill></td>
            <td class="px-3 py-2 text-right whitespace-nowrap max-md:hidden {h && h.temperature_c >= 55 ? 'text-warn' : ''}">
              {h ? `${h.temperature_c} °C` : "–"}
            </td>
            <td class="px-3 py-2 text-right whitespace-nowrap max-md:hidden">{h ? h.power_on_hours.toLocaleString() : "–"}</td>
            <td class="px-3 py-2 text-right max-md:hidden {h && h.bad_sectors > 0 ? 'text-warn' : ''}">{h ? h.bad_sectors : "–"}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/snippet}

{#if storage.data}
  <section class="mt-6" aria-labelledby="spare-h">
    <h2 id="spare-h" class="mb-2 font-semibold">
      <Help label="Spare disks and partitions">
        Disks that are not part of any pool, such as one just plugged in, and partitions that are not mounted. A partition mounts under
        /var/mnt, where stacks and backups can use it.
      </Help>
    </h2>
    {#if spare.length > 0}
      {@render diskTable(spare)}
    {/if}
    {#if spareParts.length > 0}
      <div class="relative overflow-x-auto rounded-lg border border-line bg-surface {spare.length > 0 ? 'mt-3' : ''}">
        <table class="w-full border-collapse text-left tabular-nums">
          <thead>
            <tr class="border-b border-line bg-sunken">
              <th scope="col" class="label px-3 py-2">Partition</th>
              <th scope="col" class="label px-3 py-2 whitespace-nowrap max-sm:hidden">File system</th>
              <th scope="col" class="label px-3 py-2 text-right">Size</th>
              <th scope="col" class="label px-3 py-2 max-md:hidden">Disk</th>
              <th scope="col" class="px-3 py-2"><span class="sr-only">Mount</span></th>
            </tr>
          </thead>
          <tbody>
            {#each spareParts as part (part.uuid)}
              <tr class="border-b border-line last:border-0">
                <td class="px-3 py-2 [overflow-wrap:anywhere]" title={part.path}>{part.label || part.path.replace("/dev/", "")}</td>
                <td class="px-3 py-2 max-sm:hidden">{part.fstype}</td>
                <td class="px-3 py-2 text-right whitespace-nowrap">{bytes(part.size)}</td>
                <td class="px-3 py-2 max-md:hidden">{part.disk}</td>
                <td class="px-3 py-2">
                  <form
                    class="flex flex-wrap items-center justify-end gap-x-3 gap-y-2"
                    onsubmit={(e) => {
                      e.preventDefault();
                      mountPartition(part);
                    }}
                  >
                    <label class="flex min-w-0 items-center gap-2">
                      <span class="text-muted">/var/mnt/</span>
                      <input
                        class="input w-32 min-w-0"
                        aria-label="Name for {part.label || part.path}"
                        autocomplete="off"
                        spellcheck="false"
                        pattern="[a-z0-9][a-z0-9-]*"
                        maxlength="31"
                        placeholder={part.mount_name}
                        bind:value={partNames[part.uuid]}
                      />
                    </label>
                    <label class="flex items-center gap-1.5 whitespace-nowrap">
                      <input type="checkbox" bind:checked={partReadOnly[part.uuid]} />
                      Read-only
                    </label>
                    <button type="submit" class="btn btn-primary" disabled={op.isPending}>
                      {#if mounting === part.uuid && op.isPending}<LoaderCircle class="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />Mounting…{:else}Mount{/if}
                    </button>
                  </form>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
    {#if spare.length === 0 && spareParts.length === 0}
      <p class="text-muted">None.</p>
    {/if}
  </section>
{/if}

<SystemDisk />

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

{#if confirm}
  {@const c = confirm}
  <Modal open={true} onOpenChange={(o) => !o && (confirm = null)} title={c.title} description={c.body}>
    {#if error}<p class="text-bad">{error}</p>{/if}
    {#snippet actions()}
      <button type="button" class="btn btn-danger" disabled={op.isPending} onclick={() => op.mutate(c.run)}>{c.label}</button>
    {/snippet}
  </Modal>
{/if}
