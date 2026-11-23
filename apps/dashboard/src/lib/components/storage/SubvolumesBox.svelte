<script lang="ts">
  import { LoaderCircle } from "@lucide/svelte";
  import type { Schema, Stack } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import { ago, bytes } from "#lib/format.ts";
  import { browseHref, capacity, folderName, measuredAt, segments, swatchOf, usedBy } from "#lib/storage.ts";
  import StackChips from "./StackChips.svelte";

  type Pool = Schema<"Pool">;
  type Subvolume = Schema<"Subvolume">;

  interface Props {
    /** Each mounted pool with its folders. */
    groups: { pool: Pool; folders: Subvolume[] }[];
    stacks: Stack[];
    busy: boolean;
    measuring: boolean;
    onMeasure: () => void;
    onNew: (p: Pool) => void;
    onDelete: (path: string) => void;
  }
  let { groups, stacks, busy, measuring, onMeasure, onNew, onDelete }: Props = $props();

  const all = $derived(groups.flatMap((g) => g.folders));
  /** One pool: its band is the box's own; several: a band per pool. */
  const single = $derived(groups.length === 1);
  const calculated = (folders: Subvolume[]) => {
    if (measuring) return "Recalculating…";
    const at = measuredAt(folders);
    return at ? `Calculated ${ago(at)}` : "Not calculated";
  };
</script>

{#snippet poolActions(g: { pool: Pool; folders: Subvolume[] })}
  <span class="text-xs text-muted">{calculated(g.folders)}</span>
  <button type="button" class="btn btn-sm" disabled={busy} onclick={() => onNew(g.pool)}>New subvolume…</button>
{/snippet}

<section class="overflow-hidden rounded-lg border border-line bg-surface" aria-labelledby="subvolumes-h">
  <div class="flex flex-wrap items-center gap-x-3 gap-y-1 bg-sunken px-3 py-2 {single ? 'border-b border-line' : ''}">
    <h2 id="subvolumes-h" class="min-w-0 font-semibold">
      <Help label="Subvolumes">
        Folders where stacks and backups keep files. Sizes are recalculated every few hours, before compression; data that files share
        is counted once.
      </Help>
    </h2>
    <span class="ml-auto flex flex-wrap items-center gap-2">
      {#if single}{@render poolActions(groups[0]!)}{/if}
      <button type="button" class="btn btn-sm" disabled={measuring || all.length === 0} onclick={onMeasure}>
        {#if measuring}<LoaderCircle class="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />{/if}
        Recalculate sizes
      </button>
    </span>
  </div>

  {#each groups as g (g.pool.uuid)}
    {@const cap = capacity(g.pool)}
    {@const segs = cap ? segments(g.folders, cap.total, g.pool.mountdir, cap.used) : []}
    {#if !single}
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1 border-y border-line px-3 py-1.5">
        <h3 class="font-semibold">{g.pool.name}</h3>
        <span class="text-muted">{g.pool.mountdir}</span>
        <span class="ml-auto flex flex-wrap items-center gap-2">{@render poolActions(g)}</span>
      </div>
    {/if}
    <div class="overflow-x-auto">
      <table class="w-full border-collapse text-left tabular-nums">
        <thead>
          <tr class="border-b border-line">
            <th scope="col" class="label px-3 py-2">Subvolume</th>
            <th scope="col" class="label px-3 py-2 text-right">Size</th>
            <th scope="col" class="label px-3 py-2 whitespace-nowrap max-sm:hidden">Used by</th>
            <th scope="col" class="px-3 py-2"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each g.folders as f (f.id)}
            {@const name = folderName(f.path, g.pool.mountdir)}
            {@const users = usedBy(f.path, stacks)}
            {@const swatch = swatchOf(segs, name, f.used_bytes > 0)}
            <tr class="border-b border-line last:border-0">
              <td class="px-3 py-2">
                <span class="inline-flex items-center gap-2">
                  <span aria-hidden="true" class="size-2.5 shrink-0 rounded-sm" style:background={swatch ?? "transparent"}></span>
                  <a class="link [overflow-wrap:anywhere]" href={browseHref(f.path)} title={f.path}>{name}</a>
                </span>
                {#if users.length > 0}
                  <span class="mt-1 flex flex-wrap gap-1 pl-4.5 sm:hidden"><StackChips names={users} {stacks} /></span>
                {/if}
              </td>
              <td class="px-3 py-2 text-right whitespace-nowrap">{f.used_bytes < 0 ? "–" : bytes(f.used_bytes)}</td>
              <td class="px-3 py-2 max-sm:hidden">
                {#if users.length > 0}
                  <span class="flex flex-wrap gap-1"><StackChips names={users} {stacks} /></span>
                {:else}
                  <span class="text-muted">–</span>
                {/if}
              </td>
              <td class="px-3 py-2 text-right">
                <RowMenu
                  name={f.path}
                  items={[{ label: users.length > 0 ? "Delete… (in use)" : "Delete…", danger: true, disabled: busy || users.length > 0, onSelect: () => onDelete(f.path) }]}
                />
              </td>
            </tr>
          {:else}
            <tr><td colspan="4" class="px-3 py-3 text-muted">No subvolumes yet.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/each}
</section>
