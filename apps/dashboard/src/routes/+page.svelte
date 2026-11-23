<script lang="ts">
  import { createQuery, keepPreviousData } from "@tanstack/svelte-query";
  import { ChevronRight } from "@lucide/svelte";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import { fetchStacks, idlePoll, keys } from "#lib/api/queries.ts";
  import Chart from "#lib/components/Chart.svelte";
  import Help from "#lib/components/Help.svelte";
  import Pill, { type Tone } from "#lib/components/Pill.svelte";
  import { bytes } from "#lib/format.ts";
  import { LiveMetrics } from "#lib/live-metrics.svelte.ts";
  import { byScope, CORE_HUE, latest, machineLine, mood, PAIR_HUES, PARTS, stackHues, type Frames, type Line, type Mood, type Resource } from "#lib/metrics.ts";
  import { stackTitles } from "#lib/stacks.ts";
  import { capacity } from "#lib/storage.ts";

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
  const storage = createQuery(() => ({
    queryKey: ["daemon", "storage"],
    queryFn: () => call<Schema<"StorageStatus">>(api.GET("/storage")),
    refetchInterval: 60_000,
  }));

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
  const tempDisk = $derived(latest(now.temp_disk));
  const pool = $derived((storage.data?.pools ?? []).find((p) => p.usage && p.usage.total_bytes > 0));
  const poolCap = $derived(pool ? capacity(pool) : null);

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

<section aria-label="Right now" class="mt-4 grid grid-cols-2 gap-3 md:grid-cols-4">
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
  <a class="card block hover:bg-sunken" href="/storage">
    <p class="text-muted">Storage</p>
    {#if pool && poolCap}
      <p class="mt-1 text-lg tabular-nums">{Math.round(poolCap.percent)}% used</p>
      <p class="text-xs text-muted">{bytes(poolCap.free)} free on {pool.mountdir || pool.name}</p>
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
      <Chart at={shown.at} lines={byScope(shown, "cpu", hues, title)} stacked format={pct} floor={5} ceiling={100} label="CPU usage by stack" />
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
        label="Time spent waiting for CPU, memory and disks"
      />
    </section>
    <section class="card min-w-0">
      <h2 class="font-semibold">Disks</h2>
      <Chart at={shown.at} lines={two("Read", shown.host.disk_read, "Written", shown.host.disk_write)} format={rate} floor={1e6} label="Disk reads and writes" />
    </section>
    <section class="card min-w-0">
      <h2 class="font-semibold">Network</h2>
      <Chart at={shown.at} lines={two("Received", shown.host.net_rx, "Sent", shown.host.net_tx)} format={rate} floor={1e5} label="Network traffic" />
    </section>
    <section class="card min-w-0">
      <h2 class="font-semibold">Temperature</h2>
      <Chart at={shown.at} lines={two("CPU", shown.temp_cpu, "System disk", shown.temp_disk)} format={degrees} floor={60} label="Temperatures" />
    </section>
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
