<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import { clockTime } from "#lib/format.ts";
  import PowerDialog from "#lib/components/PowerDialog.svelte";
  import { busyPoll, idlePoll } from "#lib/api/queries.ts";
  import DeployCard from "#lib/components/core/DeployCard.svelte";
  import ImageCard from "#lib/components/core/ImageCard.svelte";
  import { pendingVersion, typicalSeconds } from "#lib/components/core/core.ts";
  import { fetchPower, powerKey } from "#lib/power.ts";

  const keys = {
    deploys: ["daemon", "system-updates", "deploys"] as const,
    image: ["daemon", "system-updates", "image"] as const,
  };

  const deploys = createQuery(() => ({
    queryKey: keys.deploys,
    queryFn: () => call(api.GET("/system-updates/deploys")),
    refetchInterval: (q) => (q.state.data?.[0]?.result === "running" ? busyPoll : idlePoll),
  }));
  const latest = $derived(deploys.data?.[0]);
  const deploying = $derived(latest?.result === "running");

  // Only a host on the image has updates; anything else (a development
  // machine) refuses this.
  const image = createQuery(() => ({
    queryKey: keys.image,
    queryFn: () => call(api.GET("/system-updates/image")),
    retry: false,
    refetchInterval: deploying ? busyPoll : idlePoll,
  }));

  // An update that ended in the last ten minutes stays on the page.
  const showDeploy = $derived(
    !!latest && (deploying || (!!latest.finished_at && Date.now() - new Date(latest.finished_at).getTime() < 10 * 60_000)),
  );

  let error = $state("");
  let confirmReboot = $state(false);

  // A reboot set for a time (the install's choice) shows until it happens.
  // Read often while an update runs or has just ended: one that reboots
  // takes the page to the power page as soon as the host starts going down.
  const client = useQueryClient();
  const power = createQuery(() => ({ queryKey: powerKey, queryFn: fetchPower, refetchInterval: showDeploy ? busyPoll : idlePoll }));
  const scheduled = $derived(power.data?.scheduled);
  const cancel = createMutation(() => ({
    mutationFn: () => call(api.DELETE("/system/power/scheduled")),
    onMutate: () => (error = ""),
    onError: (e: unknown) => (error = `Cancel: ${describe(e)}`),
    onSettled: () => client.invalidateQueries({ queryKey: powerKey }),
  }));

  // An update restarts the daemon; that is expected, so the page waits and
  // does not report an error.
  const away = $derived(deploying && (!!image.error || !!deploys.error));
</script>

<svelte:head><title>Updates · System Settings · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/settings">System Settings</a> /</p>
<div class="mt-1 flex flex-wrap items-start justify-between gap-2">
  <h1 class="min-w-0 flex-1 text-xl font-semibold text-core">
    <Help label="Updates">
      The host's own software: the OS, the daemon and this dashboard. If an update does not come up healthy, the host returns to
      the version before by itself. Stacks update on their own pages.
    </Help>
  </h1>
  {#if image.data}
    <button type="button" class="btn" disabled={deploying} onclick={() => (confirmReboot = true)}>Reboot…</button>
  {/if}
</div>

{#if error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
{/if}
{#if scheduled}
  <p role="status" class="mt-4 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md bg-core-soft px-3 py-2 text-core">
    <span class="grow">{scheduled.action === "reboot" ? "Reboots" : "Shuts down"} at {clockTime(scheduled.at)}</span>
    <button type="button" class="btn btn-sm" disabled={cancel.isPending} onclick={() => cancel.mutate()}>Cancel</button>
  </p>
{/if}

{#if showDeploy && latest}
  <div class="mt-5"><DeployCard deploy={latest} {away} typical={typicalSeconds(latest, deploys.data ?? [])} /></div>
{/if}

{#if image.data}
  <ImageCard image={image.data} busy={deploying} onReboot={() => (confirmReboot = true)} />
{:else if image.error && !deploying}
  <p class="mt-5 text-muted">{describe(image.error)}</p>
{/if}

{#if confirmReboot}<PowerDialog action="reboot" to={image.data ? pendingVersion(image.data) : undefined} onClose={() => (confirmReboot = false)} />{/if}
