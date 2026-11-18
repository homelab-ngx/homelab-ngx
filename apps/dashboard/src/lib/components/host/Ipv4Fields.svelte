<script lang="ts">
  import type { Schema } from "#lib/api/client.ts";
  import { joinList, splitList } from "#lib/host.ts";

  type IPv4 = Schema<"IPv4">;

  interface Props {
    /** The setting being edited; the fields change it in place. */
    value: IPv4;
    disabled?: boolean;
  }
  let { value = $bindable(), disabled = false }: Props = $props();
  const id = $props.id();
  // Typed as text and written back as a list when the field is left, so a
  // trailing comma survives typing; a new value (a reset) shows again.
  let dnsText = $derived(joinList(value.dns ?? []));
</script>

<!-- DHCP or a static IPv4 address for the wired interfaces. -->
<fieldset class="grid gap-3" {disabled}>
  <legend class="sr-only">IPv4</legend>
  <div class="flex flex-wrap gap-4">
    <label class="flex items-center gap-2"><input type="radio" name="{id}-mode" value="dhcp" bind:group={value.mode} /> DHCP</label>
    <label class="flex items-center gap-2"><input type="radio" name="{id}-mode" value="static" bind:group={value.mode} /> Static</label>
  </div>
  {#if value.mode === "static"}
    <div class="grid gap-3 sm:grid-cols-3">
      <div class="grid gap-1">
        <label for="{id}-addr" class="text-muted">Address</label>
        <input id="{id}-addr" class="input" autocomplete="off" placeholder="192.168.1.20/24" bind:value={value.address} />
      </div>
      <div class="grid gap-1">
        <label for="{id}-gw" class="text-muted">Gateway</label>
        <input id="{id}-gw" class="input" autocomplete="off" placeholder="192.168.1.1" bind:value={value.gateway} />
      </div>
      <div class="grid gap-1">
        <label for="{id}-dns" class="text-muted">DNS</label>
        <input id="{id}-dns" class="input" autocomplete="off" placeholder="192.168.1.1" bind:value={dnsText} onchange={() => (value.dns = splitList(dnsText))} />
      </div>
    </div>
  {/if}
</fieldset>
