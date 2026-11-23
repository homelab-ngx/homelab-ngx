<script lang="ts">
  import AskButton from "#lib/components/ask/AskButton.svelte";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import ProgressBar from "#lib/components/ProgressBar.svelte";
  import StepBars from "#lib/components/StepBars.svelte";
  import { duration, shortRev } from "#lib/format.ts";
  import { clock } from "#lib/power.ts";
  import { type CoreDeploy, deployOutcome, deployRows, deployShare, deployTitle } from "./core.ts";

  interface Props {
    deploy: CoreDeploy;
    /** The daemon is not answering, which is expected while the switch restarts it. */
    away?: boolean;
    /** How long the last one of its kind took, in seconds: the bar's guide to the time left. */
    typical?: number;
  }
  let { deploy, away = false, typical }: Props = $props();

  const running = $derived(deploy.result === "running");
  const ok = $derived(deploy.result === "healthy");
  const title = $derived(deployTitle(deploy, shortRev));

  // The elapsed time moves while the deploy runs.
  let now = $state(Date.now());
  $effect(() => {
    if (!running) return;
    const tick = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(tick);
  });

  const share = $derived(deployShare(deploy, now, typical));
  const left = $derived(running && typical ? typical - (now - Date.parse(deploy.started_at)) / 1000 : 0);
</script>

<section class="card" aria-live="polite" aria-label="{title} progress">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h3 class="font-semibold">
      <Help label={title}>
        If the host does not come back healthy, it returns to the version before by itself. The update keeps running when this
        page is closed.
      </Help>
    </h3>
    <div class="flex items-center gap-2">
      {#if running}
        <Pill tone="info">{deploy.phase.toLowerCase()}</Pill>
      {:else if ok}
        <Pill tone="ok">done</Pill>
      {:else if deploy.result === "rolled_back"}
        <Pill tone="warn">went back</Pill>
      {:else}
        <Pill tone="bad">failed</Pill>
      {/if}
      {#if !running && (!ok || deploy.failed_units.length > 0)}
        <AskButton small title="Core {deploy.kind}" subject={{ kind: "core_deploy", ref: String(deploy.id) }} />
      {/if}
    </div>
  </div>
  <div class="mt-3">
    <ProgressBar value={share} label={title} tone={running ? "accent" : ok ? "ok" : "bad"} moving={running} />
  </div>
  <div class="mt-2"><StepBars rows={deployRows(deploy, running && away)} /></div>
  <p class="mt-2 text-xs text-muted tabular-nums">
    {duration(deploy.started_at, deploy.finished_at, now)}{#if left >= 1}
      · about {clock(left)} left{/if}{#if running && away}
      · the host is restarting, this page keeps checking{:else if !running}
      · {deployOutcome(deploy)}{/if}
  </p>
  {#if deploy.reason}
    <pre class="mt-2 max-h-60 overflow-auto whitespace-pre-wrap rounded-md bg-bad-soft px-3 py-2 font-mono text-xs text-bad">{deploy.reason}</pre>
  {/if}
  {#if deploy.failed_units.length > 0}
    <p class="mt-2 text-warn">
      Not started after the update: {deploy.failed_units.join(", ")}. The update stays in place.
    </p>
  {/if}
  {#if deploy.log}
    <Disclosure label="Output" class="mt-3">
      <pre class="max-h-80 overflow-auto whitespace-pre-wrap font-mono text-xs">{deploy.log}</pre>
    </Disclosure>
  {/if}
</section>
