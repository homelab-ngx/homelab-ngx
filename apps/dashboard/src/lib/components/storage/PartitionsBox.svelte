<script lang="ts">
  import type { Schema, Stack } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import { bytes } from "#lib/format.ts";
  import { browseHref, usedBy } from "#lib/storage.ts";
  import StackChips from "./StackChips.svelte";

  type Partition = Schema<"Partition">;

  interface Props {
    /** The mounted partitions. */
    partitions: Partition[];
    stacks: Stack[];
    busy: boolean;
    onAutoMount: (p: Partition, on: boolean) => void;
    onUnmount: (p: Partition) => void;
  }
  let { partitions, stacks, busy, onAutoMount, onUnmount }: Props = $props();
</script>

<section class="overflow-hidden rounded-lg border border-line bg-surface" aria-labelledby="partitions-h">
  <h2 id="partitions-h" class="border-b border-line bg-sunken px-3 py-2 font-semibold">
    <Help label="Partitions">Data on a partition is on one disk and has no parity. A pool keeps copies on other disks.</Help>
  </h2>
  <div class="overflow-x-auto">
    <table class="w-full border-collapse text-left tabular-nums">
      <thead>
        <tr class="border-b border-line">
          <th scope="col" class="label px-3 py-2">Partition</th>
          <th scope="col" class="label px-3 py-2 max-md:hidden">Device</th>
          <th scope="col" class="label px-3 py-2 text-right">Used</th>
          <th scope="col" class="label px-3 py-2 whitespace-nowrap max-sm:hidden">Used by</th>
          <th scope="col" class="px-3 py-2"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        {#each partitions as part (part.uuid)}
          {@const users = usedBy(part.mountdir, stacks)}
          <tr class="border-b border-line last:border-0">
            <td class="px-3 py-2">
              <span class="flex flex-wrap items-center gap-x-3 gap-y-1">
                <a class="link [overflow-wrap:anywhere]" href={browseHref(part.mountdir)} title={part.mountdir}>{part.mount_name}</a>
                <Pill tone="warn">no parity</Pill>
                {#if part.read_only}<Pill tone="idle">read-only</Pill>{/if}
              </span>
              {#if users.length > 0}
                <span class="mt-1 flex flex-wrap gap-1 sm:hidden"><StackChips names={users} {stacks} /></span>
              {/if}
            </td>
            <td class="px-3 py-2 text-muted max-md:hidden">{part.path.replace("/dev/", "")} · {part.fstype}</td>
            <td class="px-3 py-2 text-right whitespace-nowrap">
              {#if part.usage}
                {bytes(part.usage.total - part.usage.free)} <span class="text-muted">of {bytes(part.usage.total)}</span>
              {:else}
                {bytes(part.size)}
              {/if}
            </td>
            <td class="px-3 py-2 max-sm:hidden">
              {#if users.length > 0}
                <span class="flex flex-wrap gap-1"><StackChips names={users} {stacks} /></span>
              {:else}
                <span class="text-muted">–</span>
              {/if}
            </td>
            <td class="px-3 py-2 text-right">
              <RowMenu
                name={part.mount_name}
                items={[
                  { label: part.auto_mount ? "Do not mount at start" : "Mount at start", disabled: busy, onSelect: () => onAutoMount(part, !part.auto_mount) },
                  { label: users.length > 0 ? "Unmount… (in use)" : "Unmount…", danger: true, disabled: busy || users.length > 0, onSelect: () => onUnmount(part) },
                ]}
              />
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</section>
