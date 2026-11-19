<script lang="ts">
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import { api, call, describe } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import { busyPoll, idlePoll } from "#lib/api/queries.ts";
  import DeployCard from "#lib/components/core/DeployCard.svelte";
  import ImageCard from "#lib/components/core/ImageCard.svelte";

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

  let error = $state("");
  let confirmReboot = $state(false);

  const reboot = createMutation(() => ({
    mutationFn: () => call(api.POST("/system-updates/reboot")),
    onMutate: () => {
      error = "";
      confirmReboot = false;
    },
    onError: (e: unknown) => (error = `Reboot: ${describe(e)}`),
  }));

  // While an update or a reboot is under way the daemon goes away for a while;
  // that is expected, so the page waits and does not report an error.
  const expectedAway = $derived(deploying || reboot.isSuccess);
  const away = $derived(expectedAway && (!!image.error || !!deploys.error));
  // An update that ended in the last ten minutes stays on the page.
  const showDeploy = $derived(
    !!latest && (deploying || (!!latest.finished_at && Date.now() - new Date(latest.finished_at).getTime() < 10 * 60_000)),
  );
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
    <button type="button" class="btn" disabled={deploying || reboot.isPending || reboot.isSuccess} onclick={() => (confirmReboot = true)}>Reboot…</button>
  {/if}
</div>

{#if error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
{/if}
{#if reboot.isSuccess}
  <p role="status" class="mt-4 rounded-md bg-core-soft px-3 py-2 text-core">
    {away ? "Restarting. This page reconnects by itself." : "Rebooting. Back in a few minutes."}
  </p>
{/if}

{#if showDeploy && latest}
  <div class="mt-5"><DeployCard deploy={latest} {away} /></div>
{/if}

{#if image.data}
  <ImageCard image={image.data} busy={deploying} onReboot={() => (confirmReboot = true)} />
{:else if image.error && !expectedAway}
  <p class="mt-5 text-muted">{describe(image.error)}</p>
{/if}

<Modal
  open={confirmReboot}
  onOpenChange={(o) => (confirmReboot = o)}
  title="Reboot the homelab?"
  description="Every service, and this page, goes down for a few minutes while the host restarts."
>
  {#snippet children()}{/snippet}
  {#snippet actions()}
    <button type="button" class="btn btn-danger" disabled={reboot.isPending} onclick={() => reboot.mutate()}>Reboot now</button>
  {/snippet}
</Modal>
