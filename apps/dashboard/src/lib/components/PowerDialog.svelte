<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { api, call, describe } from "#lib/api/client.ts";
  import { fetchStacks, keys } from "#lib/api/queries.ts";
  import Modal from "#lib/components/Modal.svelte";
  import { actionLabel, fetchPower, powerHref, powerKey, type PowerAction } from "#lib/power.ts";

  interface Props {
    action: PowerAction;
    onClose: () => void;
  }
  let { action, onClose }: Props = $props();

  // Read when the dialog opens: what runs now is what a shutdown cuts off.
  const status = createQuery(() => ({ queryKey: powerKey, queryFn: fetchPower, staleTime: 0 }));
  const stacks = createQuery(() => ({ queryKey: keys.stacks, queryFn: fetchStacks }));
  const running = $derived((stacks.data ?? []).filter((s) => s.running).map((s) => s.label || s.name));
  const busy = $derived(status.data?.busy ?? []);
  /** Frozen stacks lose what they hold: they are thawed and stop cleanly, or killed. */
  const frozen = $derived(status.data?.frozen ?? []);

  let error = $state("");
  let pending = $state(false);

  /** thaw: frozen stacks stop cleanly; without it they are killed (force). */
  async function confirm(thaw: boolean) {
    const from = status.data?.boot_id;
    if (!from) return;
    // Built before onClose: closing unsets the action the parent passed.
    const href = powerHref(action, from, page.url.pathname);
    pending = true;
    error = "";
    try {
      const force = busy.length > 0 || (frozen.length > 0 && !thaw);
      await call(api.POST("/system/power", { body: { action, force, thaw } }));
      onClose();
      await goto(href);
    } catch (e) {
      error = describe(e);
      pending = false;
    }
  }

  const label = $derived(actionLabel[action] + (busy.length > 0 ? " anyway" : ""));
  const verb = $derived(actionLabel[action].toLowerCase());
</script>

<Modal
  open={true}
  onOpenChange={(o) => !o && onClose()}
  title={action === "reboot" ? "Reboot the host?" : "Shut down the host?"}
  description={action === "reboot"
    ? "The stacks stop and the pools unmount first. This page waits for the host."
    : "The stacks stop and the pools unmount first. Start it again with its power button."}
>
  {#snippet children()}
    <div class="grid gap-3">
      {#if running.length > 0}
        <p><span class="text-muted">Stops</span> {running.join(", ")}</p>
      {/if}
      {#if busy.length > 0}
        <div role="status" class="rounded-md bg-warn-soft px-3 py-2 text-warn">
          <p>Interrupted:</p>
          <ul class="mt-1 list-inside list-disc">
            {#each busy as b (b)}<li>{b}</li>{/each}
          </ul>
        </div>
      {/if}
      {#if frozen.length > 0}
        <div role="status" class="rounded-md bg-warn-soft px-3 py-2 text-warn">
          <p>Frozen: {frozen.join(", ")}</p>
          <p class="mt-1">Without a thaw they are killed and lose their memory.</p>
        </div>
      {/if}
      {#if error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
      {#if status.error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(status.error)}</p>{/if}
    </div>
  {/snippet}
  {#snippet actions()}
    {#if frozen.length > 0}
      <button type="button" class="btn btn-danger" disabled={pending || !status.data} onclick={() => confirm(false)}>{actionLabel[action]} without thawing</button>
      <button type="button" class="btn btn-primary" disabled={pending || !status.data} onclick={() => confirm(true)}>Thaw and {verb}</button>
    {:else}
      <button type="button" class="btn btn-danger" disabled={pending || !status.data} onclick={() => confirm(true)}>{label}</button>
    {/if}
  {/snippet}
</Modal>
