<script lang="ts" module>
  export interface MenuItem {
    label: string;
    onSelect: () => void;
    /** Destructive: set apart at the end, in the fault colour. */
    danger?: boolean;
    disabled?: boolean;
    /** Why a disabled item cannot be chosen. */
    title?: string;
  }
</script>

<script lang="ts">
  import { Ellipsis, EllipsisVertical } from "@lucide/svelte";
  import { DropdownMenu } from "bits-ui";

  interface Props {
    /** What the row is, for the button's spoken name. */
    name: string;
    items: MenuItem[];
    /** Dots on end: a page's or a card's menu rather than a row's. */
    vertical?: boolean;
  }
  let { name, items, vertical = false }: Props = $props();

  const plain = $derived(items.filter((i) => !i.danger));
  const danger = $derived(items.filter((i) => i.danger));
</script>

<!-- A row's rarer actions: the row keeps at most one
     button, the rest live here, the destructive ones last. -->
{#if items.length}
  <DropdownMenu.Root>
    <DropdownMenu.Trigger class={vertical ? "btn px-1.5" : "btn btn-sm px-1.5"} aria-label="More actions for {name}">
      {#if vertical}<EllipsisVertical class="size-4" aria-hidden="true" />{:else}<Ellipsis class="size-4" aria-hidden="true" />{/if}
    </DropdownMenu.Trigger>
    <DropdownMenu.Portal>
      <DropdownMenu.Content align="end" sideOffset={6} class="z-50 min-w-40 rounded-md border border-line bg-surface p-1 shadow-lg">
        {#each plain as item (item.label)}
          <DropdownMenu.Item
            disabled={item.disabled}
            title={item.title}
            onSelect={item.onSelect}
            class="cursor-default rounded px-2.5 py-1.5 outline-none data-disabled:opacity-50 data-highlighted:bg-sunken"
          >
            {item.label}
          </DropdownMenu.Item>
        {/each}
        {#if plain.length && danger.length}<DropdownMenu.Separator class="my-1 border-t border-line" />{/if}
        {#each danger as item (item.label)}
          <DropdownMenu.Item
            disabled={item.disabled}
            onSelect={item.onSelect}
            class="cursor-default rounded px-2.5 py-1.5 text-bad outline-none data-disabled:opacity-50 data-highlighted:bg-bad-soft"
          >
            {item.label}
          </DropdownMenu.Item>
        {/each}
      </DropdownMenu.Content>
    </DropdownMenu.Portal>
  </DropdownMenu.Root>
{/if}
