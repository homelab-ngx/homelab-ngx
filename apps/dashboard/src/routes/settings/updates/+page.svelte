<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe } from "#lib/api/client.ts";
  import { busyPoll, idlePoll } from "#lib/api/queries.ts";
  import PowerDialog from "#lib/components/PowerDialog.svelte";
  import StatusMark from "#lib/components/StatusMark.svelte";
  import ChangelogCard from "#lib/components/core/ChangelogCard.svelte";
  import { changelogKey, fetchChangelog } from "#lib/components/core/changelog.ts";
  import DeployCard from "#lib/components/core/DeployCard.svelte";
  import ImageCard from "#lib/components/core/ImageCard.svelte";
  import { type CoreDeploy, deployNotice, pendingVersion, typicalSeconds } from "#lib/components/core/core.ts";
  import SettingsHead from "#lib/components/settings/SettingsHead.svelte";
  import { clockTime, shortRev } from "#lib/format.ts";
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

  const changelogQuery = createQuery(() => ({
    queryKey: changelogKey,
    queryFn: fetchChangelog,
    enabled: !!image.data,
    staleTime: 5 * 60_000,
    retry: false,
  }));

  let error = $state("");
  let confirmReboot = $state(false);
  // Session-scoped tracking: a healthy op started in this tab becomes a quiet
  // one-line notice; DeployCard stays only while running or on failure.
  let trackedId = $state<number | null>(null);
  let dismissedId = $state<number | null>(null);
  let instantNotice = $state("");

  const tracked = $derived(trackedId !== null ? deploys.data?.find((d) => d.id === trackedId) : undefined);
  const failedRecent = $derived(
    !!latest &&
      latest.result !== "running" &&
      latest.result !== "healthy" &&
      dismissedId !== latest.id &&
      (!latest.finished_at || Date.now() - Date.parse(latest.finished_at) < 10 * 60_000),
  );
  const showDeploy = $derived(deploying || failedRecent);

  const notice = $derived.by(() => {
    if (deploying) return "";
    if (tracked && tracked.result === "healthy" && dismissedId !== tracked.id) {
      return deployNotice(tracked, shortRev, image.data);
    }
    return instantNotice;
  });

  function onStarted(d: CoreDeploy) {
    dismissedId = null;
    if (d.result === "healthy") {
      trackedId = null;
      instantNotice = deployNotice(d, shortRev, image.data);
    } else {
      instantNotice = "";
      trackedId = d.id;
    }
  }

  function dismissNotice() {
    if (tracked) dismissedId = tracked.id;
    instantNotice = "";
  }

  // A reboot set for a time (the install's choice) shows until it happens.
  const client = useQueryClient();

  // Refresh image and changelog immediately when a running operation finishes,
  // so buttons (Update to…, Reboot…) update without waiting for the idle poll.
  let wasDeploying = false;
  $effect(() => {
    const running = deploying;
    if (wasDeploying && !running) {
      void client.invalidateQueries({ queryKey: keys.image });
      void client.invalidateQueries({ queryKey: changelogKey });
    }
    wasDeploying = running;
  });
  const power = createQuery(() => ({ queryKey: powerKey, queryFn: fetchPower, refetchInterval: deploying ? busyPoll : idlePoll }));
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

<SettingsHead title="Updates" core>
  {#snippet help()}
    OS image, daemon and dashboard updates. Returns to the fallback automatically if an update fails to start. Stacks update on their own pages.
  {/snippet}
  {#snippet actions()}
    {#if image.data}
      <button type="button" class="btn" disabled={deploying} onclick={() => (confirmReboot = true)}>Reboot…</button>
    {/if}
  {/snippet}
</SettingsHead>

<div class="max-w-4xl">
  {#if error}
    <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
  {/if}
  {#if scheduled}
    <p role="status" class="mt-4 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md bg-core-soft px-3 py-2 text-core">
      <span class="grow">{scheduled.action === "reboot" ? "Reboots" : "Shuts down"} at {clockTime(scheduled.at)}</span>
      <button type="button" class="btn btn-sm" disabled={cancel.isPending} onclick={() => cancel.mutate()}>Cancel</button>
    </p>
  {/if}
  {#if notice}
    <p role="status" class="mt-4 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-line bg-surface px-4 py-2.5">
      <span class="inline-flex items-center gap-2"><StatusMark tone="ok" />{notice}</span>
      <button type="button" class="btn btn-sm" onclick={dismissNotice}>Dismiss</button>
    </p>
  {/if}

  {#if showDeploy && latest}
    <div class="mt-5">
      <DeployCard
        deploy={latest}
        {away}
        typical={typicalSeconds(latest, deploys.data ?? [])}
        onDismiss={!deploying ? () => (dismissedId = latest.id) : undefined}
      />
    </div>
  {/if}

  {#if image.data}
    {@const img = image.data}
    <ImageCard image={img} busy={deploying} onReboot={() => (confirmReboot = true)} {onStarted}>
      {#snippet changelog()}
        <ChangelogCard image={img} releases={changelogQuery.data?.releases ?? []} loading={changelogQuery.isPending} />
      {/snippet}
    </ImageCard>
  {:else if image.error && !deploying}
    <p class="mt-5 text-muted">{describe(image.error)}</p>
  {/if}
</div>

{#if confirmReboot}<PowerDialog action="reboot" to={image.data ? pendingVersion(image.data) : undefined} onClose={() => (confirmReboot = false)} />{/if}
