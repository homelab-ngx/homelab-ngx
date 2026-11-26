<script lang="ts">
  import type { DraftBuild, DraftCheck } from "#lib/api/client.ts";
  import { segments } from "#lib/ansi.ts";
  import { duration, shortRev } from "#lib/format.ts";
  import Pill, { type Tone } from "#lib/components/Pill.svelte";

  interface Props {
    check?: DraftCheck;
    /** The files changed after the check. */
    checkStale: boolean;
    build?: DraftBuild;
    /** The build is of another version than the one shown. */
    buildStale: boolean;
    /** Show the file a problem is in. */
    onShow: (path: string) => void;
    /** Empty the panel until the next Check or Build. */
    onClear: () => void;
  }
  let { check, checkStale, build, buildStale, onShow, onClear }: Props = $props();

  const buildTone: Record<DraftBuild["result"], Tone> = { running: "info", built: "ok", failed: "bad", interrupted: "warn" };
  const buildLabel: Record<DraftBuild["result"], string> = {
    running: "building",
    built: "built",
    failed: "failed",
    interrupted: "interrupted",
  };

  let box = $state<HTMLDivElement>();
  // While a build runs, the output follows its newest line.
  $effect(() => {
    if (build?.result === "running" && build.log && box) box.scrollTop = box.scrollHeight;
  });
</script>

<!-- The workbench's bottom panel: what Check and Build said, in one output. -->
<div class="flex h-full min-h-0 flex-col">
  <div class="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-b border-line bg-sunken px-3 py-1.5 text-xs">
    <span class="font-medium">Output</span>
    {#if check}
      <span class="flex items-center gap-1.5 text-muted">
        Check <Pill tone={check.ok ? "ok" : "bad"}>{check.ok ? "valid" : `${check.problems.length} problem${check.problems.length === 1 ? "" : "s"}`}</Pill>
        {#if checkStale}before the latest edits{:else if !check.saved}unsaved edits{/if}
      </span>
    {/if}
    {#if build}
      <span class="flex items-center gap-1.5 text-muted tabular-nums">
        Build <Pill tone={buildTone[build.result]}>{buildLabel[build.result]}</Pill>
        {shortRev(build.rev)} · {duration(build.started_at, build.finished_at)}{#if buildStale && build.result !== "running"}
          · older version{/if}
      </span>
    {/if}
    {#if check || build}
      <button type="button" class="link ml-auto text-muted" onclick={onClear}>Clear</button>
    {/if}
  </div>

  <div bind:this={box} aria-live="polite" class="min-h-0 flex-1 overflow-auto px-3 py-2">
    {#if !check && !build}
      <p class="text-muted">Check and Build print here.</p>
    {/if}
    {#if check && !check.ok}
      <ul class="mb-2 flex flex-col gap-1.5">
        {#each check.problems as p, i (i)}
          <li class="flex flex-wrap items-baseline gap-x-2">
            <button type="button" class="link shrink-0 text-xs" onclick={() => onShow(p.file)}>{p.file}</button>
            <span class="min-w-0 break-words text-bad">{p.message}</span>
          </li>
        {/each}
      </ul>
    {/if}
    {#if build}
      {#if build.result === "interrupted"}
        <p class="text-muted">The server restarted during the build.</p>
      {/if}
      {#if build.error}
        <pre class="mb-2 font-mono text-xs leading-relaxed whitespace-pre-wrap text-bad">{build.error}</pre>
      {/if}
      {#if build.log}
        <pre class="font-mono text-xs leading-relaxed whitespace-pre-wrap text-muted">{#each build.log.split("\n") as line, i (i)}{#each segments(line) as s, j (j)}<span class={s.cls}>{s.text}</span>{/each}{"\n"}{/each}</pre>
      {/if}
    {/if}
  </div>
</div>
