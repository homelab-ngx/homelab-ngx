<script lang="ts">
  import { createQuery, keepPreviousData } from "@tanstack/svelte-query";
  import { Check, ChevronRight } from "@lucide/svelte";
  import { api, call, describe, type Schema, type ServiceInfo } from "#lib/api/client.ts";
  import { fetchServices, fetchStacks, idlePoll, keys } from "#lib/api/queries.ts";
  import Chart from "#lib/components/Chart.svelte";
  import Help from "#lib/components/Help.svelte";
  import Pill, { type Tone } from "#lib/components/Pill.svelte";
  import StatusMark from "#lib/components/StatusMark.svelte";
  import UpdatedDialog from "#lib/components/core/UpdatedDialog.svelte";
  import { backupKeys, type Backup, type Coverage } from "#lib/backups.ts";
  import { bytes } from "#lib/format.ts";
  import { fetchHostSettings, hostKeys } from "#lib/host.ts";
  import { notificationKeys, type Snapshot } from "#lib/notifications.ts";
  import { notificationsTray } from "#lib/notifications-tray.svelte.ts";
  import { attentionItems, setupChecklist } from "#lib/overview.ts";
  import { profileMarks } from "#lib/power-meter.ts";
  import { watts } from "#lib/power-profile.ts";
  import { LiveMetrics } from "#lib/live-metrics.svelte.ts";
  import { byScope, CORE_HUE, latest, machineLine, mood, PAIR_HUES, PARTS, stackHues, type Frames, type Line, type Mood, type Resource } from "#lib/metrics.ts";
  import { fetchSettingsStatus, settingsStatusKey } from "#lib/settings.ts";
  import { stackTitles } from "#lib/stacks.ts";
  import { fullness, poolsSummary } from "#lib/storage.ts";

  type Range = "live" | "24h" | "7d" | "30d";
  const RANGES: { id: Range; label: string }[] = [
    { id: "live", label: "Live" },
    { id: "24h", label: "24 hours" },
    { id: "7d", label: "7 days" },
    { id: "30d", label: "30 days" },
  ];
  let range = $state<Range>("live");

  // The live stream runs while the page is visible, whatever the range: the
  // figures at the top are always "now".
  const live = new LiveMetrics();
  $effect(() => {
    const sync = () => (document.hidden ? live.close() : live.open());
    sync();
    document.addEventListener("visibilitychange", sync);
    return () => {
      document.removeEventListener("visibilitychange", sync);
      live.close();
    };
  });

  const history = createQuery(() => ({
    queryKey: ["daemon", "metrics-history", range],
    queryFn: () =>
      call<Schema<"MetricsHistory">>(
        api.GET("/metrics/history", { params: { query: { range: range === "live" ? "24h" : range } } }),
      ),
    enabled: range !== "live",
    refetchInterval: 60_000,
    placeholderData: keepPreviousData,
  }));
  const stacks = createQuery(() => ({ queryKey: keys.stacks, queryFn: fetchStacks, refetchInterval: idlePoll }));
  const services = createQuery(() => ({ queryKey: keys.services, queryFn: fetchServices, refetchInterval: idlePoll }));
  const coreServices = createQuery(() => ({
    queryKey: ["daemon", "core-services"],
    queryFn: () => call<ServiceInfo[]>(api.GET("/core-services")),
    refetchInterval: idlePoll,
    retry: false,
  }));
  const notifications = createQuery(() => ({
    queryKey: notificationKeys.snapshot,
    queryFn: () => call<Snapshot>(api.GET("/notifications")),
    refetchInterval: 30_000,
  }));
  const settingsStatus = createQuery(() => ({
    queryKey: settingsStatusKey,
    queryFn: fetchSettingsStatus,
    refetchInterval: 30_000,
  }));
  const storage = createQuery(() => ({
    queryKey: ["daemon", "storage"],
    queryFn: () => call<Schema<"StorageStatus">>(api.GET("/storage")),
    refetchInterval: 60_000,
  }));
  const hostSettings = createQuery(() => ({
    queryKey: hostKeys.settings,
    queryFn: fetchHostSettings,
    refetchInterval: 60_000,
  }));
  const backups = createQuery(() => ({
    queryKey: backupKeys.list,
    queryFn: () => call<Backup[]>(api.GET("/backups")),
    refetchInterval: 60_000,
  }));
  const coverage = createQuery(() => ({
    queryKey: backupKeys.coverage,
    queryFn: () => call<Coverage>(api.GET("/backup-coverage")),
    refetchInterval: 60_000,
  }));

  const ONBOARDING_KEY = "homelab.onboarding.dismissed";
  let onboardingDismissed = $state(readDismissed());
  function readDismissed(): boolean {
    try {
      return localStorage.getItem(ONBOARDING_KEY) === "1";
    } catch {
      return false;
    }
  }
  function dismissOnboarding() {
    onboardingDismissed = true;
    try {
      localStorage.setItem(ONBOARDING_KEY, "1");
    } catch {
      // A private window keeps dismissal for this load only.
    }
  }

  const checklist = $derived(
    onboardingDismissed
      ? []
      : setupChecklist({
          storage: storage.data,
          host: hostSettings.data,
          stacks: stacks.data,
          backups: backups.data,
        }),
  );

  const attention = $derived(
    attentionItems({
      services: services.data,
      coreServices: coreServices.data,
      notifications: notifications.data,
      stacks: stacks.data,
      settingsStatus: settingsStatus.data,
      storage: storage.data,
      coverage: coverage.data,
    }),
  );

  const now = $derived(live.frames);
  const shown: Frames | undefined = $derived(range === "live" ? live.frames : history.data?.frames);
  const reloading = $derived(range !== "live" && history.isPlaceholderData);

  // A stack keeps its colour whichever range is shown: hues follow the
  // installed stacks by name, plus any removed one still in the history.
  const hues = $derived(
    stackHues([...new Set([...(stacks.data ?? []).map((s) => s.name), ...Object.keys(shown?.stacks ?? {})])]),
  );

  const title = $derived(stackTitles(stacks.data));

  const cores = $derived(live.info?.cores ?? 0);
  const memTotal = $derived(live.info?.memory_total ?? 0);
  const machine = $derived(live.info ? machineLine(live.info) : "");
  const cpuNow = $derived(latest(now.host.cpu));
  const memNow = $derived(latest(now.host.memory));
  const tempCpu = $derived(latest(now.temp_cpu));
  const powerCpu = $derived(latest(now.power_cpu));
  const powerWall = $derived(latest(now.power_wall));
  const hasPower = $derived(powerWall !== undefined || powerCpu !== undefined);
  const tempDisk = $derived(latest(now.temp_disk));
  const storageSummary = $derived(poolsSummary(storage.data?.pools ?? []));

  const health = $derived(mood(now));
  const MOOD: Record<Mood, { tone: Tone; word: string }> = {
    idle: { tone: "ok", word: "idle" },
    light: { tone: "ok", word: "light load" },
    medium: { tone: "warn", word: "medium load" },
    heavy: { tone: "bad", word: "heavy load" },
  };
  const WAITS: Record<Resource, string> = { cpu: "CPU wait", memory: "Memory wait", disks: "Disk wait" };
  const line = $derived.by(() => {
    if (!health) return live.connected ? "Collecting readings…" : "Connecting…";
    if (health.mood === "idle" || health.mood === "light") return "";
    return `${health.waits ? WAITS[health.waits] : "CPU usage"} ${pct(health.share)}`;
  });

  const pct = (v: number) => (v > 0 && v < 0.1 ? "<0.1%" : v < 10 ? `${v.toFixed(1)}%` : `${Math.round(v)}%`);
  const rate = (v: number) => `${bytes(v)}/s`;
  const degrees = (v: number) => `${Math.round(v)} °C`;

  const two = (a: string, av: (number | null)[], b: string, bv: (number | null)[]): Line[] => [
    { label: a, hue: PAIR_HUES[0]!, values: av },
    { label: b, hue: PAIR_HUES[1]!, values: bv },
  ];
  const any = (vs: (number | null)[]) => vs.some((v) => v !== null);
  /** The meter's line and the CPU package's, each only where it has readings. */
  const powerLines = (f: Frames) => two("Wall", f.power_wall, "CPU", f.power_cpu).filter((l) => any(l.values));

  /** Rows of the "who uses what" table, from the latest live point. */
  const rows = $derived(
    byScope(now, "cpu", hues, title).map((l) => {
      const s = l.series;
      return { ...l, cpu: latest(l.values), memory: latest(s?.memory), rx: latest(s?.net_rx), tx: latest(s?.net_tx) };
    }),
  );
  /** Core's parts (byScope's last lines) fold into one row above the stacks. */
  const parts = $derived(rows.slice(-PARTS.length).map((r, i) => ({ ...r, label: PARTS[i]!.short })));
  const stackRows = $derived(rows.slice(0, -PARTS.length));
  const total = (vs: (number | undefined)[]) =>
    vs.some((v) => v !== undefined) ? vs.reduce<number>((t, v) => t + (v ?? 0), 0) : undefined;
  const core = $derived({ cpu: total(parts.map((p) => p.cpu)), memory: total(parts.map((p) => p.memory)) });
  let coreOpen = $state(false);
</script>

<svelte:head><title>Overview · Homelab-ngx</title></svelte:head>

<UpdatedDialog />

<div class="flex flex-wrap items-end justify-between gap-3">
  <div>
    <div class="flex flex-wrap items-center gap-2">
      <h1 class="text-xl font-semibold">
        <Help label="Overview">
          Over the last minute. Idle and light load: nothing waits, with CPU usage under or over 10%. Medium and heavy load: work waits
          for CPU, memory or disks.
        </Help>
      </h1>
      {#if health}
        <Pill tone={MOOD[health.mood].tone}>{MOOD[health.mood].word}</Pill>
      {/if}
    </div>
    {#if line}<p class="mt-0.5 text-muted">{line}</p>{/if}
    {#if machine}<p class="mt-0.5 text-xs text-muted">{machine}</p>{/if}
  </div>
</div>

{#if attention.length > 0}
  <section aria-label="Needs attention" class="mt-4 rounded-lg border border-line bg-surface px-3 py-2.5">
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
      <h2 class="font-semibold">Needs attention</h2>
      <ul class="flex flex-wrap items-center gap-x-4 gap-y-1.5">
        {#each attention as item (item.id)}
          <li>
            {#if item.href}
              <a
                href={item.href}
                class="inline-flex items-center gap-1.5 rounded-sm text-fg hover:text-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
              >
                <StatusMark tone={item.tone} />
                <span>{item.label}</span>
                <ChevronRight class="size-3.5 text-muted" aria-hidden="true" />
              </a>
            {:else}
              <button
                type="button"
                class="inline-flex items-center gap-1.5 rounded-sm text-fg hover:text-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                onclick={() => (notificationsTray.open = true)}
              >
                <StatusMark tone={item.tone} />
                <span>{item.label}</span>
                <ChevronRight class="size-3.5 text-muted" aria-hidden="true" />
              </button>
            {/if}
          </li>
        {/each}
      </ul>
    </div>
  </section>
{/if}

{#if checklist.length > 0}
  <section aria-label="Get started" class="card mt-4">
    <div class="flex items-center justify-between gap-2">
      <h2 class="font-semibold">Get started</h2>
      <button type="button" class="btn btn-sm" onclick={dismissOnboarding}>Dismiss</button>
    </div>
    <ul class="mt-3 grid gap-2 sm:grid-cols-2">
      {#each checklist as step (step.id)}
        <li>
          <a
            href={step.href}
            class="flex items-center justify-between gap-2 rounded-md border border-line px-3 py-2 transition-colors hover:border-muted focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
          >
            <span class="flex items-center gap-2 {step.done ? 'text-muted line-through' : 'text-fg'}">
              {#if step.done}
                <Check class="size-4 shrink-0 text-ok" aria-hidden="true" />
              {:else}
                <span class="size-4 shrink-0 rounded-full border border-muted" aria-hidden="true"></span>
              {/if}
              <span>{step.label}</span>
            </span>
            {#if !step.done}<ChevronRight class="size-3.5 shrink-0 text-muted" aria-hidden="true" />{/if}
          </a>
        </li>
      {/each}
    </ul>
  </section>
{/if}

<section aria-label="Right now" class="mt-4 grid grid-cols-2 gap-3 md:grid-cols-4 {hasPower ? 'lg:grid-cols-5' : ''}">
  <div class="card">
    <p class="text-muted">CPU usage</p>
    <p class="mt-1 text-lg tabular-nums">{cpuNow === undefined ? "–" : pct(cpuNow)}</p>
    <p class="text-xs text-muted">{cores ? `of ${cores} cores` : ""}</p>
  </div>
  <div class="card">
    <p class="text-muted">Memory</p>
    <p class="mt-1 text-lg tabular-nums">{memNow === undefined ? "–" : bytes(memNow)}</p>
    <p class="text-xs text-muted">{memTotal ? `of ${bytes(memTotal)}` : ""}</p>
  </div>
  <div class="card">
    <p class="text-muted">Temperature</p>
    <p class="mt-1 text-lg tabular-nums">{tempCpu === undefined ? "–" : degrees(tempCpu)}</p>
    <p class="text-xs text-muted">CPU{tempDisk === undefined ? "" : ` · system disk ${degrees(tempDisk)}`}</p>
  </div>
  {#if hasPower}
    <a
      class="card group block transition-colors hover:border-muted focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
      href="/settings/system#profile"
    >
      <p class="flex items-center justify-between text-muted">
        <span>Power</span>
        <ChevronRight class="size-3.5 text-muted transition-colors group-hover:text-fg" aria-hidden="true" />
      </p>
      <p class="mt-1 text-lg tabular-nums">{watts(powerWall ?? powerCpu)}</p>
      <p class="text-xs text-muted">
        {powerWall === undefined ? "CPU package" : powerCpu === undefined ? "at the wall" : `at the wall · CPU ${watts(powerCpu)}`}
      </p>
    </a>
  {/if}
  <a
    class="card group block transition-colors hover:border-muted focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
    href="/storage"
  >
    <p class="flex items-center justify-between text-muted">
      <span>Storage</span>
      <ChevronRight class="size-3.5 text-muted transition-colors group-hover:text-fg" aria-hidden="true" />
    </p>
    {#if storageSummary}
      {@const sTone = fullness(storageSummary.maxPercent)}
      <p class="mt-1 text-lg tabular-nums {sTone === 'bad' ? 'text-bad' : sTone === 'warn' ? 'text-warn' : ''}">{Math.round(storageSummary.percent)}% used</p>
      <p class="text-xs text-muted">
        {#if storageSummary.singlePool}
          {bytes(storageSummary.free)} free on {storageSummary.singlePool.mountdir || storageSummary.singlePool.name}
        {:else}
          {bytes(storageSummary.free)} free across {storageSummary.count} pools
        {/if}
      </p>
    {:else}
      <p class="mt-1 text-lg">–</p>
      <p class="text-xs text-muted">
        {storage.isPending ? "" : storage.data?.pools.some((p) => p.state === "mounted") ? "size not available" : "no pool mounted"}
      </p>
    {/if}
  </a>
</section>

<div class="mt-5 flex flex-wrap items-center gap-2" role="group" aria-label="Time range">
  {#each RANGES as r (r.id)}
    <button
      type="button"
      class="btn btn-sm {range === r.id ? 'border-accent bg-accent-soft text-accent' : ''}"
      aria-pressed={range === r.id}
      onclick={() => (range = r.id)}
    >
      {r.label}
    </button>
  {/each}
  <span class="text-xs text-muted">
    {range === "live" ? "15 minutes · every 2 s" : range === "24h" ? "every minute" : range === "7d" ? "every 5 minutes" : "every 30 minutes"}
  </span>
</div>

{#if history.error && range !== "live"}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(history.error)}</p>
{/if}

{#if shown && shown.at.length > 1}
  <div class="mt-4 grid grid-cols-1 gap-3 md:grid-cols-2 {reloading ? 'opacity-60' : ''}">
    <section class="card min-w-0">
      <h2 class="font-semibold">
        <Help label="CPU usage">
          Share of total CPU ({cores || "all"} cores) each stack used. Core is the host itself: System Services are the
          system's services and this dashboard, User Sessions are logins and what they start, Kernel is the system's own work.
        </Help>
      </h2>
      <Chart at={shown.at} lines={byScope(shown, "cpu", hues, title)} stacked format={pct} floor={5} ceiling={100} sync="overview" label="CPU usage by stack" />
    </section>
    <section class="card min-w-0">
      <h2 class="font-semibold">
        <Help label="Memory">
          Memory each stack uses, without file cache the system can hand back at once. Core (Kernel) is memory the system keeps for
          itself, much of it the file system's own cache.
        </Help>
      </h2>
      <Chart
        at={shown.at}
        lines={byScope(shown, "memory", hues, title)}
        stacked
        format={bytes}
        floor={1e9}
        ceiling={memTotal || undefined}
        sync="overview"
        label="Memory use by stack"
      />
    </section>
    <section class="card min-w-0">
      <h2 class="font-semibold">
        <Help label="Waiting">
          Share of the time some work waited for CPU, memory or disks. Short spikes are normal; a line that stays up means
          the host is short of that resource.
        </Help>
      </h2>
      <Chart
        at={shown.at}
        lines={[...two("CPU", shown.host.pressure_cpu, "Memory", shown.host.pressure_memory), { label: "Disks", hue: PAIR_HUES[2]!, values: shown.host.pressure_io }]}
        format={pct}
        floor={5}
        ceiling={100}
        sync="overview"
        label="Time spent waiting for CPU, memory and disks"
      />
    </section>
    <section class="card min-w-0">
      <h2 class="font-semibold">Disks</h2>
      <Chart at={shown.at} lines={two("Read", shown.host.disk_read, "Written", shown.host.disk_write)} format={rate} floor={1e6} sync="overview" label="Disk reads and writes" />
    </section>
    <section class="card min-w-0">
      <h2 class="font-semibold">Network</h2>
      <Chart at={shown.at} lines={two("Received", shown.host.net_rx, "Sent", shown.host.net_tx)} format={rate} floor={1e5} sync="overview" label="Network traffic" />
    </section>
    <section class="card min-w-0">
      <h2 class="font-semibold">Temperature</h2>
      <Chart at={shown.at} lines={two("CPU", shown.temp_cpu, "System disk", shown.temp_disk)} format={degrees} floor={60} sync="overview" label="Temperatures" />
    </section>
    {#if powerLines(shown).length}
      <section class="card min-w-0">
        <h2 class="font-semibold">
          <Help label="Power">Wall is the power meter's reading of the whole machine, CPU the processor alone. A dashed line marks a profile change.</Help>
        </h2>
        <Chart
          at={shown.at}
          lines={powerLines(shown)}
          format={watts}
          floor={10}
          marks={range === "live" ? [] : profileMarks(history.data?.profile_changes)}
          sync="overview"
          label="Power draw"
        />
      </section>
    {/if}
  </div>
{:else if range !== "live" && history.isPending}
  <p class="mt-4 text-muted">Loading…</p>
{:else if range !== "live"}
  <div class="card mt-4 text-center text-muted">Nothing stored for this range yet.</div>
{/if}

{#if rows.length > 1}
  <section class="mt-5" aria-labelledby="who">
    <h2 id="who" class="font-semibold">Now, by stack</h2>
    <div class="mt-2 overflow-x-auto rounded-lg border border-line bg-surface">
      <table class="w-full text-left">
        <thead class="bg-sunken">
          <tr>
            <th class="label px-3 py-2">Stack</th>
            <th class="label px-3 py-2 text-right">CPU</th>
            <th class="label px-3 py-2 text-right">Memory</th>
            <th class="label px-3 py-2 text-right max-md:hidden">Network</th>
          </tr>
        </thead>
        <tbody>
          <tr class="border-t border-line">
            <td class="px-3 py-2">
              <span class="inline-flex items-center gap-2">
                <span aria-hidden="true" class="swatch size-2.5 rounded-full" style:--light={CORE_HUE.light} style:--dark={CORE_HUE.dark}></span>
                <button
                  type="button"
                  class="inline-flex items-center gap-1.5 hover:text-accent"
                  aria-expanded={coreOpen}
                  aria-controls={PARTS.map((p) => `part-${p.key}`).join(" ")}
                  onclick={() => (coreOpen = !coreOpen)}
                >
                  Core
                  <ChevronRight class="size-3.5 text-muted transition-transform {coreOpen ? 'rotate-90' : ''}" aria-hidden="true" />
                </button>
              </span>
            </td>
            <td class="px-3 py-2 text-right tabular-nums">{core.cpu === undefined ? "–" : pct(core.cpu)}</td>
            <td class="px-3 py-2 text-right tabular-nums">{core.memory === undefined ? "–" : bytes(core.memory)}</td>
            <td class="px-3 py-2 text-right tabular-nums max-md:hidden">–</td>
          </tr>
          {#each parts as r, i (r.label)}
            <tr id="part-{PARTS[i]!.key}" class="border-t border-line bg-sunken" hidden={!coreOpen}>
              <td class="py-1.5 pr-3 pl-8">
                <span class="inline-flex items-center gap-2">
                  <span aria-hidden="true" class="swatch size-2 rounded-full" style:--light={r.hue.light} style:--dark={r.hue.dark}></span>
                  {r.label}
                </span>
              </td>
              <td class="px-3 py-1.5 text-right tabular-nums">{r.cpu === undefined ? "–" : pct(r.cpu)}</td>
              <td class="px-3 py-1.5 text-right tabular-nums">{r.memory === undefined ? "–" : bytes(r.memory)}</td>
              <td class="px-3 py-1.5 text-right tabular-nums max-md:hidden">–</td>
            </tr>
          {/each}
          {#each stackRows as r (r.label)}
            <tr class="border-t border-line">
              <td class="px-3 py-2">
                <span class="inline-flex items-center gap-2">
                  <span aria-hidden="true" class="swatch size-2.5 rounded-full" style:--light={r.hue.light} style:--dark={r.hue.dark}></span>
                  {#if r.href}<a class="link" href={r.href}>{r.label}</a>{:else}{r.label}{/if}
                </span>
              </td>
              <td class="px-3 py-2 text-right tabular-nums">{r.cpu === undefined ? "–" : pct(r.cpu)}</td>
              <td class="px-3 py-2 text-right tabular-nums">{r.memory === undefined ? "–" : bytes(r.memory)}</td>
              <td class="px-3 py-2 text-right tabular-nums max-md:hidden">
                {r.rx === undefined ? "–" : `↓ ${rate(r.rx)} · ↑ ${rate(r.tx ?? 0)}`}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>
{/if}

<style>
  .swatch {
    background: var(--light);
  }
  :global(.dark) .swatch {
    background: var(--dark);
  }
</style>
