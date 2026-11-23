<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { prefersReducedMotion, Tween } from "svelte/motion";
  import { goto, replaceState } from "$app/navigation";
  import { page } from "$app/state";
  import { api, call, DEMO } from "#lib/api/client.ts";
  import { fetchHostSettings, hostKeys } from "#lib/host.ts";
  import type { CoreDeploy } from "#lib/components/core/core.ts";
  import PowerSteps from "#lib/components/power/PowerSteps.svelte";
  import PowerTrack from "#lib/components/power/PowerTrack.svelte";
  import ProbeStrip from "#lib/components/power/ProbeStrip.svelte";
  import { start } from "#lib/power-start.svelte.ts";
  import {
    fetchPower,
    gone,
    installRows,
    powerRows,
    powerView,
    probe,
    stuck,
    timer,
    trackOf,
    type PowerAction,
    type PowerStatus,
    type Probe,
  } from "#lib/power.ts";

  // The address holds what the page needs, so a reload during the reboot
  // picks up where it was: the action, the boot it began in, the page to go
  // back to, the version to come back on, whether an install runs first,
  // when it was asked for, and what the page has seen since.
  const q = page.url.searchParams;
  const action: PowerAction = q.get("action") === "poweroff" ? "poweroff" : "reboot";
  const from = q.get("from") ?? "";
  const back = q.get("back")?.startsWith("/") ? q.get("back")! : "/";
  const to = q.get("to") ?? "";
  const installs = q.get("install") === "1";
  const since = Number(q.get("t")) || Date.now();
  let downSince = $state<number | undefined>(Number(q.get("down")) || undefined);
  let sawReady = $state(q.get("ready") === "1");
  let past = $state(q.get("past") === "1");
  let deployId = $state(Number(q.get("deploy")) || 0);

  function remember(key: string, value: string) {
    const u = new URL(page.url.href);
    if (u.searchParams.get(key) === value) return;
    u.searchParams.set(key, value);
    replaceState(u, {});
  }
  $effect(() => remember("t", String(since)));

  // The request this page was opened for, while this tab still holds it.
  const mine = $derived(from && start.from === from ? start : undefined);
  $effect(() => {
    if (mine?.deploy) remember("deploy", String(mine.deploy));
  });

  function wentDown() {
    if (downSince) return;
    downSince = Date.now();
    remember("down", String(downSince));
  }

  // The daemon keeps the host up until a read like this one has seen it
  // ready, so the page can say every step finished.
  function ready() {
    wentDown();
    if (sawReady) return;
    sawReady = true;
    remember("ready", "1");
  }

  // The install is over and the shutdown its reboot begins is next: from
  // here a daemon that does not answer is the host going down, not the
  // install restarting it.
  function pastInstall() {
    if (past) return;
    past = true;
    remember("past", "1");
  }

  /** The install this run follows: the one the request started, or after a reload the newest for this version. */
  function pick(list: CoreDeploy[]): CoreDeploy | undefined {
    const id = mine?.deploy || deployId;
    if (id) return list.find((d) => d.id === id);
    if (mine?.pending) return undefined;
    const d = list.find((x) => x.kind === "install" && (!to || x.version === to));
    if (d) deployId = d.id;
    return d;
  }

  // Every read answers: a host that is down is a result, not an error.
  let last = $state<PowerStatus | undefined>();
  let deploy = $state<CoreDeploy | undefined>();
  let probes = $state<Probe[]>([]);
  let checks = $state(0);
  /** Checks without an answer in a row, up to the last one. */
  let misses = $state(0);
  let missed = $state(false);
  const read = createQuery(() => ({
    queryKey: ["daemon", "system", "power", "follow"],
    queryFn: async () => {
      const following = installs && !past;
      const [p, d] = await Promise.allSettled([fetchPower(), following ? call(api.GET("/system-updates/deploys")) : Promise.resolve(undefined)]);
      checks++;
      if (p.status === "rejected") {
        if (!following) wentDown();
        misses++;
        missed = true;
        probes = probe(probes, "miss");
        return { reachable: false, status: undefined };
      }
      const s = p.value;
      const ours = s.boot_id === from || !from;
      if (d.status === "fulfilled" && d.value) deploy = pick(d.value) ?? deploy;
      if (ours && (deploy?.result === "healthy" || (s.phase !== "idle" && s.phase !== "failed"))) pastInstall();
      if (s.phase === "going_down" && ours) ready();
      if (ours) last = s;
      probes = probe(probes, ours ? "answer" : "back");
      misses = 0;
      return { reachable: true, status: s };
    },
    refetchInterval: 1000,
    refetchIntervalInBackground: true,
    retry: false,
    gcTime: 0,
  }));

  let now = $state(Date.now());
  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  const reachable = $derived(read.data?.reachable ?? false);
  const expected = $derived(last?.expected_seconds ?? 120);
  const view = $derived(
    powerView({
      action,
      from,
      status: read.data?.status ?? last,
      reachable,
      checked: read.data !== undefined,
      downSince,
      now,
      expected,
      start: mine,
      install: installs ? { deploy, past } : undefined,
    }),
  );
  const hostGone = $derived(gone(view.stage));
  // The steps stay on the page once the host is gone.
  const steps = $derived(view.stage === "back" || view.steps.length === 0 ? (last?.steps ?? []) : view.steps);
  const rows = $derived([
    ...(installs ? installRows(deploy, !reachable && !past) : []),
    ...(view.stage === "installing" || view.stage === "install_failed" ? [] : powerRows(steps, hostGone)),
  ]);
  const marks = $derived(trackOf(action, view.stage, last?.phase, sawReady, installs ? { past } : undefined));
  const tm = $derived(timer(view, { since, downSince, now, expected }));
  const ended = $derived(stuck(view.stage));
  const settled = $derived(ended || view.stage === "back" || view.stage === "off");

  // The overall bar glides and never goes back. While the countdown runs it
  // moves for the whole second between two ticks, so it never stops.
  const bar = new Tween(0);
  $effect(() => {
    const v = Math.max(bar.target, view.share);
    if (v !== bar.target) void bar.set(v, { duration: prefersReducedMotion.current ? 0 : 1000 });
  });

  // The host's name and the version it runs now, from what the pages before
  // already read; and once it is back, the version it came back on.
  const host = createQuery(() => ({ queryKey: hostKeys.settings, queryFn: fetchHostSettings, retry: false, staleTime: Infinity }));
  const image = createQuery(() => ({
    queryKey: ["daemon", "system-updates", "image"],
    queryFn: () => call(api.GET("/system-updates/image")),
    enabled: !!to,
    retry: false,
    staleTime: Infinity,
  }));
  const isBack = $derived(view.stage === "back");
  const after = createQuery(() => ({
    queryKey: ["daemon", "system-updates", "image", "after", from],
    queryFn: () => call(api.GET("/system-updates/image")),
    enabled: !!to && isBack,
    retry: false,
  }));
  const was = $derived(image.data && image.data.booted !== to ? image.data.booted : "");
  const landed = $derived(after.data?.booted ?? "");

  // Back: a full load, so an update's new dashboard is the one that runs.
  // The demo keeps its made-up host only while the page is not reloaded.
  $effect(() => {
    if (!isBack) return;
    const t = setTimeout(() => (DEMO ? void goto(back, { replaceState: true }) : location.replace(back)), 2500);
    return () => clearTimeout(t);
  });

  const title = installs ? "Updating" : action === "reboot" ? "Rebooting" : "Shutting down";
  const line = $derived(
    {
      starting: "Starting.",
      refused: view.error,
      installing: reachable ? `Installing ${to}.` : "Restarting the daemon.",
      install_failed: view.error,
      stopping: "Stopping the stacks and unmounting the pools.",
      failed: view.error,
      ready: action === "reboot" ? "Ready. Rebooting now." : "Ready. Powering off now.",
      down: "Away while it restarts.",
      late: "Taking longer than usual. Still checking.",
      back: landed && to && landed !== to ? `Back on ${landed}; ${to} did not start.` : landed ? `Back on ${landed}.` : "Back.",
      off: "Off. Start it again with its power button.",
    }[view.stage],
  );
</script>

<svelte:head><title>{title} · Homelab-ngx</title></svelte:head>

<header class="text-center">
  {#if host.data?.host.hostname}<p class="text-xs text-muted">{host.data.host.hostname}</p>{/if}
  <h1 class="text-xl font-semibold text-core">{title}</h1>
  {#if to}<p class="mt-0.5 text-muted tabular-nums">{was ? `${was} → ` : ""}{to}</p>{/if}
</header>

<section class="card mt-6 grid gap-5" aria-label="{title} progress">
  <div class="flex items-end justify-between gap-3">
    <p class="flex items-baseline gap-2" aria-hidden="true">
      <span class="text-4xl font-semibold tracking-tight tabular-nums">{tm.value}</span>
      <span class="text-xs text-muted">{tm.label}</span>
    </p>
    <p class="text-muted tabular-nums" aria-hidden="true">{Math.round(bar.current * 100)}%</p>
  </div>

  <div
    class="relative -mt-2 h-2 overflow-hidden rounded-full bg-sunken"
    role="progressbar"
    aria-label={title}
    aria-valuemin={0}
    aria-valuemax={100}
    aria-valuenow={Math.round(view.share * 100)}
  >
    <div
      class="relative h-full overflow-hidden rounded-full {ended ? 'bg-bad' : view.stage === 'back' ? 'bg-ok' : 'bg-accent'} {view.stage === 'late'
        ? 'motion-safe:animate-pulse'
        : ''} transition-colors"
      style:width="{Math.max(bar.current, 0.02) * 100}%"
    >
      {#if !settled}
        <span class="absolute inset-y-0 left-0 w-1/3 bg-linear-to-r from-transparent via-surface/45 to-transparent motion-safe:animate-sheen motion-reduce:hidden"></span>
      {/if}
    </div>
  </div>

  <PowerTrack {marks} />

  {#if ended}
    <div role="alert" class="grid gap-2 rounded-md bg-bad-soft px-3 py-2 text-bad">
      <p>{line}</p>
      {#if view.stage === "install_failed" && deploy?.reason}
        <pre class="max-h-40 overflow-auto font-mono text-xs whitespace-pre-wrap">{deploy.reason}</pre>
      {/if}
    </div>
    <div class="flex justify-end"><a class="btn" href={back}>Back</a></div>
  {:else}
    <p role="status" class="text-center">{line}</p>
  {/if}

  {#if missed && !ended}
    <ProbeStrip {probes} total={checks} {misses} />
  {/if}
</section>

{#if rows.length > 0}
  <section class="card mt-4 py-1.5" aria-label="Steps">
    <PowerSteps {rows} />
  </section>
{/if}
