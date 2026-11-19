<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe } from "#lib/api/client.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { busyPoll, idlePoll } from "#lib/api/queries.ts";
  import DeployCard from "#lib/components/core/DeployCard.svelte";
  import ImageCard from "#lib/components/core/ImageCard.svelte";
  import { KIND_MARK, eventTitle, type UpdateEvent } from "#lib/components/core/core.ts";
  import { ago, shortRev, stamp } from "#lib/format.ts";

  const keys = {
    status: ["daemon", "system-updates", "status"] as const,
    version: ["daemon", "version"] as const,
    timeline: (page: number) => ["daemon", "system-updates", "timeline", page] as const,
    deploys: ["daemon", "system-updates", "deploys"] as const,
  };

  const deploys = createQuery(() => ({
    queryKey: keys.deploys,
    queryFn: () => call(api.GET("/system-updates/deploys")),
    refetchInterval: (q) => (q.state.data?.[0]?.result === "running" ? busyPoll : idlePoll),
  }));
  const latest = $derived(deploys.data?.[0]);
  const deploying = $derived(latest?.result === "running");

  const status = createQuery(() => ({
    queryKey: keys.status,
    queryFn: () => call(api.GET("/system-updates/status")),
    refetchInterval: deploying ? busyPoll : idlePoll,
  }));
  const version = createQuery(() => ({ queryKey: keys.version, queryFn: () => call(api.GET("/version")) }));
  // An image host (#97) answers this; a NixOS host refuses it.
  const image = createQuery(() => ({
    queryKey: ["daemon", "system-updates", "image"],
    queryFn: () => call(api.GET("/system-updates/image")),
    retry: false,
    refetchInterval: deploying ? busyPoll : idlePoll,
  }));

  let page = $state(1);
  const timeline = createQuery(() => ({
    queryKey: keys.timeline(page),
    queryFn: () => call(api.GET("/system-updates/timeline", { params: { query: { page, days: 14 } } })),
    refetchInterval: 30_000,
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

  const client = useQueryClient();
  let confirmRollback = $state(false);
  const startDeploy = (path: "/system-updates/deploy" | "/system-updates/rollback", what: string) =>
    createMutation(() => ({
      mutationFn: () => call(api.POST(path)),
      onMutate: () => {
        error = "";
        confirmRollback = false;
      },
      onError: (e: unknown) => (error = `${what}: ${describe(e)}`),
      onSettled: () => client.invalidateQueries({ queryKey: keys.deploys }),
    }));
  const deploy = startDeploy("/system-updates/deploy", "Update");
  const rollback = startDeploy("/system-updates/rollback", "Go back");

  const s = $derived(status.data);
  const core = $derived(s?.core);
  // While a deploy or a reboot is under way the daemon goes away for a while;
  // that is expected, so the page waits and does not report an error.
  const expectedAway = $derived(deploying || reboot.isSuccess);
  const away = $derived(expectedAway && (!!status.error || !!deploys.error));
  // A deploy that ended in the last ten minutes stays on the page.
  const showDeploy = $derived(
    !!latest && (deploying || (!!latest.finished_at && Date.now() - new Date(latest.finished_at).getTime() < 10 * 60_000)),
  );
  const events = $derived((timeline.data?.days ?? []).flatMap((d) => d.events));
  const pages = $derived(timeline.data?.pages ?? 0);

  /** What a history row says beside its time. */
  const eventSummary = (ev: UpdateEvent) =>
    [eventTitle(ev), ev.generation ? `#${ev.generation}${ev.current ? " · live" : ""}` : "", ev.nixos_version ?? ""].filter(Boolean).join(" · ");
</script>

<svelte:head><title>Updates · System Settings · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/settings">System Settings</a> /</p>
<div class="mt-1 flex flex-wrap items-start justify-between gap-2">
  <h1 class="text-xl font-semibold text-core">
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
{:else}
<section class="card mt-5 border-core/40" aria-labelledby="system-h">
  <h2 id="system-h" class="font-semibold text-core">
    <Help label="System">An update takes a few minutes and briefly restarts the dashboard. Stacks keep running.</Help>
  </h2>
  {#if status.error && !s}
    <p class="mt-2 {expectedAway ? 'text-muted' : 'text-bad'}">{expectedAway ? "Waiting for the host…" : describe(status.error)}</p>
  {:else}
    <dl class="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-4">
      <div>
        <dt class="label">NixOS</dt>
        <dd class="mt-0.5">{s?.release.current ?? "–"}{s?.release.pinned ? " · pre-release" : ""}</dd>
      </div>
      <div>
        <dt class="label">Generation</dt>
        <dd class="mt-0.5">{s?.generation ? `#${s.generation}` : "–"}</dd>
      </div>
      <div>
        <dt class="label">Daemon</dt>
        <dd class="mt-0.5">{version.data?.version ?? "–"}</dd>
      </div>
      {#if core?.configured}
        <div>
          <dt class="label">Core</dt>
          <dd class="mt-0.5">{core.running_rev || "–"}{core.update_available ? ` → ${shortRev(core.main_rev)}` : ""}</dd>
        </div>
      {/if}
    </dl>

    {#if s}
      <div class="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-line pt-3">
        {#if core?.configured}
          <Pill tone={core.update_available ? "info" : "ok"}>{core.update_available ? "update ready" : "up to date"}</Pill>
        {/if}
        {#if s.reboot_required}
          <span class="flex flex-wrap items-center gap-x-2">
            <Pill tone="warn">reboot needed</Pill>
            <span class="text-muted">newer kernel installed</span>
          </span>
        {/if}
        <div class="ml-auto flex flex-wrap gap-2">
          <button type="button" class="btn" disabled={reboot.isPending || reboot.isSuccess} onclick={() => (confirmReboot = true)}>Reboot…</button>
          {#if core?.configured}
            <button type="button" class="btn" disabled={deploying || deploy.isPending} onclick={() => (confirmRollback = true)}>Go back one version…</button>
            <button type="button" class="btn {core.update_available ? 'btn-primary' : ''}" disabled={deploying || deploy.isPending} onclick={() => deploy.mutate()}>
              {deploying ? "Updating…" : core.update_available ? "Update core" : "Update anyway"}
            </button>
          {/if}
        </div>
      </div>
    {/if}

    {#if s?.upgrade}
      <p class="mt-3 flex flex-wrap items-center gap-x-2">
        <Pill tone="info">upgrade</Pill>
        NixOS {s.upgrade.release}{s.upgrade.pre_release ? " · pre-release" : ""}
        <a class="link text-xs" href={s.upgrade.notes_url} target="_blank" rel="noreferrer">Release notes ↗</a>
      </p>
      <p class="mt-1 text-xs text-muted">
        One-way: services may migrate their data. Set the flake's nixpkgs input to nixos-{s.upgrade.release} and deploy.
      </p>
    {:else if s}
      <p class="mt-3 text-xs text-muted">No newer NixOS release{s.checked_at ? ` · checked ${ago(s.checked_at)}` : ""}</p>
    {/if}
  {/if}
</section>

<section class="mt-8" aria-labelledby="history-h">
  <h2 id="history-h" class="mb-2 font-semibold">History</h2>
  {#if timeline.error}
    <p class="text-bad">{describe(timeline.error)}</p>
  {/if}
  <ul class="flex flex-col gap-2">
    {#each events as ev (ev.id)}
      <li class="card py-2.5">
        <Disclosure label={stamp(ev.time)} summary={eventSummary(ev)}>
          {#if ev.changes.length > 0}
            <ul class="max-h-80 overflow-y-auto text-xs">
              {#each ev.changes as c (`${c.tier}:${c.name}:${c.kind}:${c.from}`)}
                <li class="py-0.5">
                  <span class="text-muted">{KIND_MARK[c.kind] ?? "~"}</span>
                  {c.name}
                  <span class="text-muted">{c.from}{c.from && c.to ? " → " : ""}</span>{c.to}
                </li>
              {/each}
            </ul>
            {#if ev.dependencies > 0}<p class="mt-1 text-xs text-muted">{ev.dependencies} dependencies not shown.</p>{/if}
          {:else}
            <p class="text-xs text-muted">{ev.baseline ? "Nothing earlier to compare with." : "No package changes to list."}</p>
          {/if}
        </Disclosure>
      </li>
    {:else}
      <li class="text-muted">{timeline.isLoading ? "Loading…" : "No generations recorded yet."}</li>
    {/each}
  </ul>
  {#if pages > 1}
    <nav aria-label="History pages" class="mt-3 flex items-center justify-between">
      <button type="button" class="btn btn-sm" disabled={page <= 1} onclick={() => page--}>‹ Newer</button>
      <span class="text-xs text-muted">Page {page} of {pages}</span>
      <button type="button" class="btn btn-sm" disabled={page >= pages} onclick={() => page++}>Older ›</button>
    </nav>
  {/if}
</section>
{/if}

<Modal
  open={confirmRollback}
  onOpenChange={(o) => (confirmRollback = o)}
  title="Go back one version?"
  description="Core switches to the version before the last update. The dashboard restarts briefly; stacks keep running."
>
  {#snippet children()}{/snippet}
  {#snippet actions()}
    <button type="button" class="btn btn-danger" disabled={rollback.isPending} onclick={() => rollback.mutate()}>Go back</button>
  {/snippet}
</Modal>

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
