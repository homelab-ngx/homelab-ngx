<script lang="ts">
  import { LoaderCircle } from "@lucide/svelte";
  import type { Schema } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { bytes } from "#lib/format.ts";
  import { diskState } from "#lib/storage.ts";

  type Disk = Schema<"Disk">;
  type Partition = Schema<"Partition">;

  interface Props {
    /** Disks in no pool. */
    disks: Disk[];
    /** Partitions that are not mounted. */
    partitions: Partition[];
    busy: boolean;
    /** The partition whose Mount is running, by uuid. */
    mounting: string;
    /** Absent when the page offers it already (no pool yet). */
    onCreatePool?: () => void;
    onMount: (p: Partition, name: string, readOnly: boolean) => void;
  }
  let { disks, partitions, busy, mounting, onCreatePool, onMount }: Props = $props();

  /** What is typed and ticked for each partition, by uuid. */
  const names = $state<Record<string, string>>({});
  const readOnly = $state<Record<string, boolean>>({});
</script>

<section class="overflow-hidden rounded-lg border border-line bg-surface" aria-labelledby="spare-h">
  <div class="flex flex-wrap items-center gap-2 border-b border-line bg-sunken px-3 py-2">
    <h2 id="spare-h" class="min-w-0 font-semibold">
      <Help label="Spare disks and partitions">
        Disks that are not part of any pool, such as one just plugged in, and partitions that are not mounted. A pool erases what a spare
        disk holds. A partition mounts under /var/mnt, where stacks and backups can use it.
      </Help>
    </h2>
    {#if disks.length > 0 && onCreatePool}
      <button type="button" class="btn btn-sm ml-auto" onclick={onCreatePool}>Create pool…</button>
    {/if}
  </div>

  {#if disks.length > 0}
    <div class="overflow-x-auto">
      <table class="w-full border-collapse text-left tabular-nums">
        <thead>
          <tr class="border-b border-line">
            <th scope="col" class="label px-3 py-2">Disk</th>
            <th scope="col" class="label px-3 py-2 max-sm:hidden">Holds</th>
            <th scope="col" class="label px-3 py-2">Health</th>
            <th scope="col" class="label px-3 py-2 text-right max-md:hidden">Temp</th>
            <th scope="col" class="label px-3 py-2 text-right max-md:hidden">Hours on</th>
            <th scope="col" class="label px-3 py-2 text-right max-md:hidden">Bad sectors</th>
            <th scope="col" class="label px-3 py-2 text-right">Size</th>
          </tr>
        </thead>
        <tbody>
          {#each disks as d (d.path)}
            {@const st = diskState(d)}
            {@const h = d.health}
            <tr class="border-b border-line last:border-0">
              <td class="px-3 py-2">
                <span>{d.name}</span>
                <span class="ml-1.5 text-muted max-sm:ml-0 max-sm:block max-sm:text-xs">{d.friendly_name || d.model || ""}</span>
              </td>
              <td class="px-3 py-2 max-sm:hidden {d.contents ? '' : 'text-muted'}">{d.contents || "nothing"}</td>
              <td class="px-3 py-2"><Pill tone={st.tone}>{st.word}</Pill></td>
              <td class="px-3 py-2 text-right whitespace-nowrap max-md:hidden {h && h.temperature_c >= 55 ? 'text-warn' : ''}">{h ? `${h.temperature_c} °C` : "–"}</td>
              <td class="px-3 py-2 text-right whitespace-nowrap max-md:hidden">{h ? h.power_on_hours.toLocaleString() : "–"}</td>
              <td class="px-3 py-2 text-right max-md:hidden {h && h.bad_sectors > 0 ? 'text-warn' : ''}">{h ? h.bad_sectors : "–"}</td>
              <td class="px-3 py-2 text-right whitespace-nowrap">{bytes(d.size)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  {#if partitions.length > 0}
    <div class="overflow-x-auto {disks.length > 0 ? 'border-t border-line' : ''}">
      <table class="w-full border-collapse text-left tabular-nums">
        <thead>
          <tr class="border-b border-line">
            <th scope="col" class="label px-3 py-2">Partition</th>
            <th scope="col" class="label px-3 py-2 whitespace-nowrap max-sm:hidden">File system</th>
            <th scope="col" class="label px-3 py-2 text-right">Size</th>
            <th scope="col" class="label px-3 py-2 max-md:hidden">Disk</th>
            <th scope="col" class="px-3 py-2"><span class="sr-only">Mount</span></th>
          </tr>
        </thead>
        <tbody>
          {#each partitions as part (part.uuid)}
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
                    onMount(part, names[part.uuid]?.trim() ?? "", !!readOnly[part.uuid]);
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
                      bind:value={names[part.uuid]}
                    />
                  </label>
                  <label class="flex items-center gap-1.5 whitespace-nowrap">
                    <input type="checkbox" bind:checked={readOnly[part.uuid]} />
                    Read-only
                  </label>
                  <button type="submit" class="btn btn-primary" disabled={busy}>
                    {#if mounting === part.uuid && busy}<LoaderCircle class="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />Mounting…{:else}Mount{/if}
                  </button>
                </form>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  {#if disks.length === 0 && partitions.length === 0}
    <p class="px-3 py-2 text-muted">None.</p>
  {/if}
</section>
