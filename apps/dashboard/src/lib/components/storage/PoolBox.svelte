<script lang="ts">
  import { LoaderCircle } from "@lucide/svelte";
  import type { Schema } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import Switch from "#lib/components/Switch.svelte";
  import { bytes } from "#lib/format.ts";
  import { capacity, diskState, fullness, poolBlocked, removal, role, running, segmentColor, segments } from "#lib/storage.ts";

  type Pool = Schema<"Pool">;
  type Disk = Schema<"Disk">;

  interface Props {
    pool: Pool;
    /** The pool's folders: the bar is split by them. */
    folders: Schema<"Subvolume">[];
    /** Disks, the removal's place among them, and the pool's actions; off shows the space alone. */
    advanced: boolean;
    /** A storage request is running: the actions wait. */
    busy: boolean;
    /** This pool's Mount is the request running. */
    mounting: boolean;
    onMount: (name: string) => void;
    onAutoMount: (on: boolean) => void;
    onAddDisk: () => void;
    onRemoveDisk: (d: Disk) => void;
    onUnmount: () => void;
    onDelete: () => void;
    /** Stops a removal, or clears one that ended. */
    onEndRemoval: () => void;
  }
  let { pool: p, folders, advanced, busy, mounting, onMount, onAutoMount, onAddDisk, onRemoveDisk, onUnmount, onDelete, onEndRemoval }: Props = $props();

  const mounted = $derived(p.state === "mounted");
  const cap = $derived(capacity(p));
  const segs = $derived(cap ? segments(folders, cap.total, p.mountdir, cap.used) : []);
  const blocked = $derived(poolBlocked(p));
  const leaving = $derived(running(p.operation) ? p.operation?.disk : undefined);
  /** The name typed for the mount; none typed is the pool's own. */
  let name = $state("");

  const roleWord = { cache: "Cache", storage: "Storage", "": "–" } as const;
  const toneText = { ok: "text-muted", warn: "text-warn", bad: "text-bad", info: "text-muted", idle: "text-muted" } as const;
</script>

{#snippet removalLine(o: NonNullable<Pool["operation"]>, track = "bg-sunken")}
  {@const r = removal(o, bytes)}
  <div role="status">
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
      <span class="tabular-nums {o.phase === 'failed' ? 'text-bad' : ''}">{r.text}</span>
      {#if running(o)}
        <div class="h-1.5 min-w-40 flex-1 overflow-hidden rounded-full {track}" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={r.percent} aria-label="Data moved off the disk">
          <div class="h-full rounded-full bg-accent transition-[width] duration-700" style:width="{r.percent}%"></div>
        </div>
      {:else}
        <span class="flex-1"></span>
      {/if}
      <button type="button" class="btn btn-sm" disabled={busy || (running(o) && o.phase !== "moving")} onclick={onEndRemoval}>
        {running(o) ? "Stop" : "Dismiss"}
      </button>
    </div>
    {#if !running(o) && o.error}<p class="mt-1 text-sm text-muted [overflow-wrap:anywhere]">{o.error}</p>{/if}
  </div>
{/snippet}

<section class="overflow-hidden rounded-lg border border-line bg-surface" aria-label={p.mountdir || p.name}>
  <div class="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-line bg-sunken px-3 py-2">
    <h2 class="font-semibold">{p.name}</h2>
    {#if p.mountdir}<span class="text-muted">{p.mountdir}</span>{/if}
    <Pill tone={mounted ? "ok" : "idle"}>{mounted ? "mounted" : "unmounted"}</Pill>
    <span class="ml-auto text-xs text-muted">
      <Help label={p.data_replicas > 1 ? `${p.data_replicas} copies of each file` : "1 copy of each file"}>
        {#if p.data_replicas > 1}
          Each copy is on a different disk, so one disk can fail without losing files. Sizes on this page count one copy.
        {:else}
          If a disk fails, the files on it are gone and only backups still have them.
        {/if}
      </Help>
    </span>
  </div>

  <div class="px-3 py-3">
    {#if !mounted}
      <form
        class="flex flex-wrap items-center justify-end gap-3"
        onsubmit={(e) => {
          e.preventDefault();
          onMount(name.trim());
        }}
      >
        <label class="flex min-w-0 items-center gap-2">
          <span class="text-muted">/var/mnt/</span>
          <input
            class="input w-40 min-w-0"
            aria-label="Name"
            autocomplete="off"
            spellcheck="false"
            pattern="[a-z0-9][a-z0-9-]*"
            maxlength="31"
            placeholder={p.mount_name}
            bind:value={name}
          />
        </label>
        <button type="submit" class="btn btn-primary" disabled={busy}>
          {#if mounting && busy}<LoaderCircle class="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />Mounting…{:else}Mount{/if}
        </button>
      </form>
    {:else if cap}
      <div class="flex flex-wrap items-center gap-x-4 gap-y-2 tabular-nums">
        <div class="flex h-2 min-w-48 flex-1 overflow-hidden rounded-full bg-sunken" role="img" aria-label="{Math.round(cap.percent)}% used">
          {#if segs.length > 0}
            {#each segs as s, i (s.label)}
              <span class="h-full not-first:border-l not-first:border-surface" style:width="{s.percent}%" style:background={segmentColor(i, s.other)}></span>
            {/each}
          {:else}
            <span class="h-full bg-accent" style:width="{cap.percent}%"></span>
          {/if}
        </div>
        <span class="whitespace-nowrap">{bytes(cap.used)} <span class="text-muted">used of {bytes(cap.total)}</span></span>
        <span class="whitespace-nowrap {toneText[fullness(cap.percent)]}">{bytes(cap.free)} free</span>
      </div>
    {:else}
      <p class="text-muted">The pool's size could not be read.</p>
    {/if}
    <!-- With the disks hidden, a removal shows here: it is long, and it can be stopped. -->
    {#if p.operation && (!advanced || !p.disks.some((d) => d.path === p.operation?.disk))}
      <div class="mt-3 border-t border-line pt-3">{@render removalLine(p.operation)}</div>
    {/if}
  </div>

  {#if advanced}
    <div class="overflow-x-auto border-t border-line">
      <table class="w-full border-collapse text-left tabular-nums">
        <thead>
          <tr class="border-b border-line">
            <th scope="col" class="label px-3 py-2">Disk</th>
            <th scope="col" class="label px-3 py-2 max-md:hidden">
              <Help label="Role">New files land on the cache disks and move to the storage disks later.</Help>
            </th>
            <th scope="col" class="label px-3 py-2">Health</th>
            <th scope="col" class="label px-3 py-2 text-right max-md:hidden">Temp</th>
            <th scope="col" class="label px-3 py-2 text-right max-md:hidden">Hours on</th>
            <th scope="col" class="label px-3 py-2 text-right max-md:hidden">
              <Help label="Bad sectors">Spots that stopped reading reliably. A few can be harmless; a growing number means the disk is wearing out.</Help>
            </th>
            <th scope="col" class="label px-3 py-2 text-right">Size</th>
            {#if mounted}<th scope="col" class="px-3 py-2"><span class="sr-only">Actions</span></th>{/if}
          </tr>
        </thead>
        <tbody>
          {#each p.disks as d (d.path)}
            {@const st = diskState(d)}
            {@const h = d.health}
            <tr class="border-b border-line last:border-0">
              <td class="px-3 py-2">
                <span>{d.name}</span>
                <span class="ml-1.5 text-muted max-sm:ml-0 max-sm:block max-sm:text-xs">{d.friendly_name || d.model || ""}</span>
              </td>
              <td class="px-3 py-2 whitespace-nowrap max-md:hidden">{roleWord[role(p, d)]}</td>
              <td class="px-3 py-2">
                {#if leaving === d.path}<Pill tone="info">leaving</Pill>{:else}<Pill tone={st.tone}>{st.word}</Pill>{/if}
              </td>
              <td class="px-3 py-2 text-right whitespace-nowrap max-md:hidden {h && h.temperature_c >= 55 ? 'text-warn' : ''}">{h ? `${h.temperature_c} °C` : "–"}</td>
              <td class="px-3 py-2 text-right whitespace-nowrap max-md:hidden">{h ? h.power_on_hours.toLocaleString() : "–"}</td>
              <td class="px-3 py-2 text-right max-md:hidden {h && h.bad_sectors > 0 ? 'text-warn' : ''}">{h ? h.bad_sectors : "–"}</td>
              <td class="px-3 py-2 text-right whitespace-nowrap">{bytes(d.size)}</td>
              {#if mounted}
                <td class="px-3 py-2 text-right">
                  <RowMenu name={d.name} items={[{ label: "Remove from pool…", danger: true, disabled: running(p.operation), onSelect: () => onRemoveDisk(d) }]} />
                </td>
              {/if}
            </tr>
            {#if p.operation && p.operation.disk === d.path}
              <tr class="border-b border-line bg-sunken last:border-0">
                <td colspan="8" class="px-3 py-2 sm:pl-8">{@render removalLine(p.operation, "bg-surface")}</td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>

    <div class="flex flex-wrap items-center gap-x-6 gap-y-2 border-t border-line px-3 py-2.5">
      <span class="flex items-center gap-2">
        <span aria-hidden="true">Mount at startup</span>
        <Switch checked={p.auto_mount} label="Mount {p.name} at startup" disabled={busy} onChange={onAutoMount} />
      </span>
      {#if blocked}<span class="text-xs text-muted">{blocked}</span>{/if}
      <span class="ml-auto flex flex-wrap gap-2">
        {#if mounted}
          <button type="button" class="btn btn-sm" disabled={busy || !!blocked} title={blocked || undefined} onclick={onAddDisk}>Add disk…</button>
          <button type="button" class="btn btn-sm btn-danger" disabled={busy || !!blocked} title={blocked || undefined} onclick={onUnmount}>Unmount…</button>
        {/if}
        <button type="button" class="btn btn-sm btn-danger" disabled={busy || !!blocked} title={blocked || undefined} onclick={onDelete}>Delete pool…</button>
      </span>
    </div>
  {/if}
</section>
