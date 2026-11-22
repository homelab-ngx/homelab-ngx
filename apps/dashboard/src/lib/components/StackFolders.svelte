<script lang="ts">
  import { useQueryClient } from "@tanstack/svelte-query";
  import { FolderOpen } from "@lucide/svelte";
  import { api, call, type Schema, type Stack } from "#lib/api/client.ts";
  import { keys } from "#lib/api/queries.ts";
  import { browseHref } from "#lib/storage.ts";
  import Disclosure from "./Disclosure.svelte";
  import Pill from "./Pill.svelte";
  import RowMenu, { type MenuItem } from "./RowMenu.svelte";

  type Folder = Schema<"StackFolder">;
  type FolderIn = Schema<"StackFolderIn">;

  interface Props {
    stack: Stack;
  }
  let { stack }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  const folders = $derived(stack.folders ?? []);

  let pending = $state(false);
  let error = $state("");
  let note = $state("");

  let newPath = $state("");
  let newMount = $state("");
  let newReadOnly = $state(true);

  const asIn = (f: Folder): FolderIn => ({ path: f.path, mount: f.mount, read_only: f.read_only });

  /** Every change sends the whole list; the daemon checks each folder and mount. */
  async function save(list: FolderIn[], done: string): Promise<boolean> {
    pending = true;
    error = note = "";
    try {
      const s = await call<Stack>(api.PUT("/stacks/{name}/folders", { params: { path: { name: stack.name } }, body: { folders: list } }));
      client.setQueryData([...keys.stacks, stack.name], s);
      note = done;
      return true;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
      return false;
    } finally {
      pending = false;
      void client.invalidateQueries({ queryKey: keys.stacks });
    }
  }

  async function add(e: SubmitEvent) {
    e.preventDefault();
    const path = newPath.trim();
    const mount = newMount.trim();
    if (!path || !mount) return;
    if (await save([...folders.map(asIn), { path, mount, read_only: newReadOnly }], `${path} added at ${mount}.`)) {
      newPath = newMount = "";
      newReadOnly = true;
    }
  }

  function menuOf(f: Folder): MenuItem[] {
    const others = (keep: (o: Folder) => boolean) => folders.filter(keep).map(asIn);
    return [
      {
        label: f.read_only ? "Allow writes" : "Make read-only",
        onSelect: () =>
          void save(
            folders.map((o) => (o.mount === f.mount ? { ...asIn(o), read_only: !f.read_only } : asIn(o))),
            `${f.mount} ${f.read_only ? "writable" : "read-only"}.`,
          ),
      },
      { label: "Remove", danger: true, onSelect: () => void save(others((o) => o.mount !== f.mount), `${f.mount} removed. The folder stays.`) },
    ];
  }
</script>

<section class="card" aria-label="Added folders">
  <Disclosure section label="Added folders" summary={String(folders.length)} remember="stack-folders">
    {#snippet help()}
      Host folders bound into this stack on top of what it asks for, with no change to its repo. Read-only unless writes are
      allowed. Changes apply at the next restart.
    {/snippet}
    {#if folders.length}
      <ul class="divide-y divide-line">
        {#each folders as f (f.mount)}
          <li class="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 py-2 lg:grid-cols-[minmax(0,16rem)_7rem_minmax(0,1fr)_4rem]">
            <span class="break-all">{f.mount}</span>
            {#if f.shadowed_by}
              <Pill tone="warn">not bound</Pill>
            {:else}
              <Pill tone="ok">{f.read_only ? "read-only" : "writable"}</Pill>
            {/if}
            <span class="break-all text-muted">{f.path}{f.shadowed_by ? ` · overlaps ${f.shadowed_by}` : ""}</span>
            <span class="flex justify-end gap-1">
              <a class="btn btn-sm px-1.5" href={browseHref(f.path)} aria-label="Browse {f.path}" title="Browse files">
                <FolderOpen class="size-4" aria-hidden="true" />
              </a>
              <RowMenu name={f.mount} items={menuOf(f)} />
            </span>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="text-muted">No added folders.</p>
    {/if}

    <form id="{id}-add" class="mt-2 flex flex-wrap items-center gap-2 border-t border-line pt-3" onsubmit={add}>
      <input
        class="input w-auto min-w-0 flex-[2] basis-56"
        aria-label="Host folder"
        placeholder="/var/mnt/pool/…"
        autocomplete="off"
        spellcheck="false"
        bind:value={newPath}
      />
      <input
        class="input w-auto min-w-0 flex-1 basis-40"
        aria-label="Mount in the stack"
        placeholder="/mnt/library"
        autocomplete="off"
        spellcheck="false"
        bind:value={newMount}
      />
      <label class="flex items-center gap-1.5">
        <input type="checkbox" bind:checked={newReadOnly} />
        Read-only
      </label>
      <button type="submit" class="btn btn-sm" disabled={pending || !newPath.trim() || !newMount.trim()}>Add</button>
    </form>
    {#if error}<p role="alert" class="mt-2 text-bad">{error}</p>{/if}
    {#if note}<p role="status" class="mt-2 text-muted">{note}</p>{/if}
  </Disclosure>
</section>
