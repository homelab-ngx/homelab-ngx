<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe } from "#lib/api/client.ts";
  import PowerDialog from "#lib/components/PowerDialog.svelte";
  import SettingsHead from "#lib/components/settings/SettingsHead.svelte";
  import { clockTime } from "#lib/format.ts";
  import { fetchPower, powerKey, type PowerAction } from "#lib/power.ts";
  import { settingsStatusKey } from "#lib/settings.ts";

  const client = useQueryClient();
  const power = createQuery(() => ({ queryKey: powerKey, queryFn: fetchPower, refetchInterval: 30_000 }));
  const scheduled = $derived(power.data?.scheduled);
  let action = $state<PowerAction | null>(null);
  let error = $state("");
  const cancel = createMutation(() => ({
    mutationFn: () => call(api.DELETE("/system/power/scheduled")),
    onMutate: () => (error = ""),
    onError: (e: unknown) => (error = `Cancel: ${describe(e)}`),
    onSettled: () => Promise.all([client.invalidateQueries({ queryKey: powerKey }), client.invalidateQueries({ queryKey: settingsStatusKey })]),
  }));
</script>

<SettingsHead title="Power">
  {#snippet help()}Reboot or shut down the host. The stacks stop first and the pools are unmounted, then the host goes down.{/snippet}
</SettingsHead>

{#if error}<p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
{#if scheduled}
  <p role="status" class="mt-4 flex max-w-4xl flex-wrap items-center gap-x-3 gap-y-2 rounded-md bg-accent-soft px-3 py-2 text-accent">
    <span class="grow">{scheduled.action === "reboot" ? "Reboots" : "Shuts down"} at {clockTime(scheduled.at)}</span>
    <button type="button" class="btn btn-sm" disabled={cancel.isPending} onclick={() => cancel.mutate()}>Cancel</button>
  </p>
{/if}

<div class="mt-5 flex flex-wrap gap-2">
  <button type="button" class="btn" onclick={() => (action = "reboot")}>Reboot…</button>
  <button type="button" class="btn" onclick={() => (action = "poweroff")}>Shut down…</button>
</div>

{#if action}<PowerDialog {action} onClose={() => (action = null)} />{/if}
