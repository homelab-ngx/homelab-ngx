<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { goto, replaceState } from "$app/navigation";
  import { page } from "$app/state";
  import Pill from "#lib/components/Pill.svelte";
  import { clock, fetchPower, gone, milestones, powerView, stepState, type PowerAction, type PowerStatus } from "#lib/power.ts";

  // The address holds what the page needs, so a reload during the reboot
  // picks up where it was: the action, the boot it began in, the page to go
  // back to, whether it was seen ready and, once the host is gone, when it
  // went.
  const q = page.url.searchParams;
  const action: PowerAction = q.get("action") === "poweroff" ? "poweroff" : "reboot";
  const from = q.get("from") ?? "";
  const back = q.get("back")?.startsWith("/") ? q.get("back")! : "/";
  let downSince = $state<number | undefined>(Number(q.get("down")) || undefined);
  let sawReady = $state(q.get("ready") === "1");

  function remember(key: string, value: string) {
    const u = new URL(page.url.href);
    u.searchParams.set(key, value);
    replaceState(u, {});
  }

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

  // Every read answers: a host that is down is a result, not an error.
  let last = $state<PowerStatus | undefined>();
  const read = createQuery(() => ({
    queryKey: ["daemon", "system", "power", "follow"],
    queryFn: async () => {
      try {
        const s = await fetchPower();
        if (s.phase === "going_down" && s.boot_id === from) ready();
        if (s.boot_id === from || !from) last = s;
        return { reachable: true, status: s };
      } catch {
        wentDown();
        return { reachable: false, status: undefined };
      }
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

  const view = $derived(
    powerView({
      action,
      from,
      status: read.data?.status ?? last,
      reachable: read.data?.reachable ?? false,
      downSince,
      now,
      expected: last?.expected_seconds ?? 120,
    }),
  );
  // The steps stay on the page once the host is gone.
  const steps = $derived(view.stage === "back" ? (last?.steps ?? []) : view.steps.length > 0 ? view.steps : (last?.steps ?? []));
  const hostGone = $derived(gone(view.stage));
  const marks = $derived(milestones(action, view.stage, last?.phase, sawReady));
  const markTone = { done: "ok", active: "info", waiting: "idle" } as const;

  // A boolean, so the clock's tick does not start the wait again.
  const isBack = $derived(view.stage === "back");
  $effect(() => {
    if (!isBack) return;
    const t = setTimeout(() => void goto(back, { replaceState: true }), 1500);
    return () => clearTimeout(t);
  });

  const title = action === "reboot" ? "Rebooting" : "Shutting down";
  const line = $derived(
    {
      stopping: "Stopping the stacks and unmounting the pools.",
      failed: view.error,
      ready: action === "reboot" ? "Ready. Rebooting now." : "Ready. Powering off now.",
      down: action === "reboot" ? `Back in about ${clock(view.left)}.` : "",
      late: "Taking longer than usual. Still checking.",
      back: "Back. Opening the page again.",
      off: "Off. Start it again with its power button.",
    }[view.stage],
  );
</script>

<svelte:head><title>{title} · Homelab-ngx</title></svelte:head>

<h1 class="text-xl font-semibold text-core">{title}</h1>

<section class="mt-5 max-w-xl rounded-lg border border-line bg-surface p-4" aria-live="polite">
  <ol class="mb-3 flex flex-wrap gap-x-5 gap-y-1" aria-label="Progress">
    {#each marks as m (m.label)}
      <li aria-current={m.state === "active" ? "step" : undefined}><Pill tone={markTone[m.state]}>{m.label}</Pill></li>
    {/each}
  </ol>
  {#if view.stage === "failed"}
    <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{line}</p>
  {:else}
    <p role="status">{line}</p>
  {/if}
  {#if action === "reboot" && (view.stage === "down" || view.stage === "late" || view.stage === "back")}
    <div
      class="mt-3 h-1.5 overflow-hidden rounded-full bg-sunken"
      role="progressbar"
      aria-label="Reboot"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(view.done * 100)}
    >
      <div class="h-full rounded-full bg-accent transition-[width] duration-1000 ease-linear {view.stage === 'late' ? 'motion-safe:animate-pulse' : ''}" style:width="{view.done * 100}%"></div>
    </div>
  {/if}
  {#if steps.length > 0}
    <ul class="mt-4 divide-y divide-line border-t border-line">
      {#each steps as s (s.kind + s.name)}
        {@const st = stepState(s, hostGone)}
        <li class="flex min-h-9 items-center gap-3 py-1.5" title={s.detail || undefined}>
          <span class="min-w-0 flex-1 truncate">{s.name}</span>
          <Pill tone={st.tone}>{st.word}</Pill>
        </li>
      {/each}
    </ul>
  {/if}
</section>
