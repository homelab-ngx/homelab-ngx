<script lang="ts">
  import { untrack } from "svelte";
  import { createMutation, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import { ago } from "#lib/format.ts";
  import { headline, runningVersion, updateOf, VERSION_STATE } from "./core.ts";

  interface Props {
    image: Schema<"ImageStatus">;
    /** An operation is running. */
    busy: boolean;
    onReboot: () => void;
  }
  let { image, busy, onReboot }: Props = $props();
  const id = $props.id();

  const client = useQueryClient();
  let error = $state("");
  let installing = $state(false);
  let when = $state<"later" | "now" | "at">("later");
  let at = $state("03:30");

  const op = createMutation(() => ({
    mutationFn: (run: () => Promise<unknown>) => run(),
    onMutate: () => (error = ""),
    onError: (e: unknown) => (error = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: ["daemon", "system-updates"] }),
  }));
  const build = () => op.mutate(() => call(api.POST("/system-updates/image/build")));
  const download = () => op.mutate(() => call(api.POST("/system-updates/image/download")));
  // One operation installs what was downloaded: the image, then the core.
  // Only a new image has a reboot to choose.
  const update = $derived(updateOf(image));
  const head = $derived(headline(image));
  function install() {
    installing = false;
    const reboot = !update?.reboot || when === "later" ? "" : when === "now" ? "now" : at;
    op.mutate(() => call(api.POST("/system-updates/image/release", { body: { reboot } })));
  }
  const boot = (version: string, mode: "once" | "default") => op.mutate(() => call(api.POST("/system-updates/image/boot", { body: { version, mode } })));
  const bootMenu = (version: string) => [
    { label: "Boot once", onSelect: () => boot(version, "once") },
    { label: "Boot by default", onSelect: () => boot(version, "default") },
  ];

  // The time a release installs by itself: a draft of the stored one, saved
  // when the box or the time changes.
  let auto = $state(untrack(() => image.auto_install_at !== undefined && image.auto_install_at !== ""));
  let autoAt = $state(untrack(() => image.auto_install_at || "03:30"));
  const saveAuto = () => op.mutate(() => call(api.PUT("/system-updates/image/auto-install", { body: { at: auto ? autoAt : "" } })));
  function toggleAuto() {
    auto = !auto;
    saveAuto();
  }

  // The version it fell back from is said once, in its own line.
  const versions = $derived(image.installed.filter((v) => v.version !== image.fell_back_from));
  // A core updated on its own runs a version the image does not have.
  const coreApart = $derived(image.core.active && image.core.active !== image.booted ? image.core.active : "");
</script>

<section class="card mt-5 border-core/40" aria-labelledby="{id}-h">
  <h2 id="{id}-h" class="font-semibold text-core">
    <Help label="Version">
      A new version is downloaded and checked first, then installed beside the running one. If it does not start, the host
      returns to the version before by itself. Stacks are not changed.
    </Help>
  </h2>

  <div class="mt-2 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
    <p class="flex flex-wrap items-center gap-x-2">
      <Pill tone={head.tone}>{head.state}</Pill>
      {#if head.detail}<span>{head.detail}</span>{/if}
    </p>
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn" disabled={busy || op.isPending} onclick={download}>Check now</button>
      {#if image.pending_reboot}
        <button type="button" class="btn btn-primary" disabled={busy} onclick={onReboot}>Reboot…</button>
      {:else if update}
        <button type="button" class="btn btn-primary" disabled={busy || op.isPending} onclick={() => (update.reboot ? (installing = true) : install())}>
          Update to {update.version}{update.reboot ? "…" : ""}
        </button>
      {/if}
    </div>
  </div>

  <dl class="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-3">
    <div>
      <dt class="label">Running</dt>
      <dd class="mt-0.5">{runningVersion(image) || "–"}</dd>
    </div>
    <div>
      <dt class="label">Newest release</dt>
      <dd class="mt-0.5">{image.release.latest || "–"}</dd>
    </div>
    <div>
      <dt class="label">Checked</dt>
      <dd class="mt-0.5">{image.release.checked_at ? ago(image.release.checked_at) : "–"}</dd>
    </div>
  </dl>
  {#if image.release.error}<p class="mt-2 text-muted">Check failed: {image.release.error}</p>{/if}
  {#if image.boot_check}
    <p role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">
      <Help label="Start check failed">This version ran before, so the host stayed up instead of restarting. Repair the cause, or boot another version under Versions on this host.</Help>: {image.boot_check}
    </p>
  {/if}
  {#if image.fell_back_from}
    <p class="mt-3 flex flex-wrap items-center gap-x-2">
      <Pill tone="warn">went back</Pill><span class="grow">{image.fell_back_from} did not start</span>
      <RowMenu name={image.fell_back_from} items={bootMenu(image.fell_back_from)} />
    </p>
  {/if}
  {#if image.core.fell_back_from}
    <p class="mt-3 flex flex-wrap items-center gap-x-2"><Pill tone="warn">went back</Pill>{image.core.fell_back_from} did not start</p>
  {/if}

  {#if error}<p role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}

  <div class="mt-3 flex flex-wrap items-center justify-between gap-2 border-t border-line pt-3">
    <Help label="Update automatically">A downloaded version is installed at this time of day, and the host reboots when the update needs it. Off: only from this page.</Help>
    <div class="flex items-center gap-2">
      <input type="time" class="input w-32" aria-label="Time of the automatic update" bind:value={autoAt} onchange={saveAuto} disabled={!auto || op.isPending} />
      <button
        type="button"
        role="switch"
        aria-checked={auto}
        aria-label="Update automatically"
        disabled={op.isPending}
        onclick={toggleAuto}
        class="relative h-4 w-7 shrink-0 rounded-full transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:opacity-50 {auto ? 'bg-accent' : 'bg-line'}"
      >
        <span aria-hidden="true" class="absolute top-0.5 size-3 rounded-full bg-surface transition-[left] {auto ? 'left-3.5' : 'left-0.5'}"></span>
      </button>
    </div>
  </div>
</section>

<section class="card mt-4">
  <Disclosure section label="Versions on this host" summary="{versions.length} installed" remember="updates-versions">
    {#snippet help()}The host keeps the running version and the one before it. The fallback is what it returns to when a new version does not start.{/snippet}
    <ul class="flex flex-col divide-y divide-line">
      {#each versions as v (v.version)}
        {@const state = VERSION_STATE[v.state] ?? { tone: "idle" as const, label: v.state }}
        <li class="flex min-h-10 items-center gap-x-3 py-1.5">
          <span class="min-w-0 grow">
            {v.version}
            {#if image.oneshot === v.version}<span class="text-muted">· boots once</span>{/if}
            {#if image.default === v.version}<span class="text-muted">· boots by default</span>{/if}
          </span>
          <span class="w-24 shrink-0"><Pill tone={state.tone}>{state.label}</Pill></span>
          <span class="flex w-8 shrink-0 justify-end">
            {#if v.version !== image.booted}<RowMenu name={v.version} items={bootMenu(v.version)} />{/if}
          </span>
        </li>
      {/each}
      {#if coreApart}
        <li class="flex min-h-10 items-center gap-x-3 py-1.5">
          <span class="min-w-0 grow">{coreApart} <span class="text-muted">· dashboard</span></span>
          <span class="w-24 shrink-0"><Pill tone="ok">running</Pill></span>
          <span class="w-8 shrink-0"></span>
        </li>
      {/if}
    </ul>
    {#if image.default}
      <div class="mt-2 flex justify-end">
        <button type="button" class="btn btn-sm" disabled={busy || op.isPending} onclick={() => boot("", "default")}>Boot the newest by default</button>
      </div>
    {/if}
  </Disclosure>
</section>

{#if image.source}
  <section class="card mt-4">
    <Disclosure section label="Development" summary={image.source} remember="updates-development">
      {#snippet help()}Builds the newest code from the source repository on this host and offers it as an update. It takes a while.{/snippet}
      <div class="flex justify-end">
        <button type="button" class="btn" disabled={busy || op.isPending} onclick={build}>Build from main</button>
      </div>
    </Disclosure>
  </section>
{/if}

<Modal
  open={installing}
  onOpenChange={(o) => (installing = o)}
  title="Update to {update?.version ?? ""}?"
  description="The dashboard restarts now. The rest of the update starts at a reboot."
>
  {#snippet children()}
    <fieldset class="grid gap-2">
      <legend class="mb-1 text-muted">Reboot</legend>
      <label class="flex items-center gap-2"><input type="radio" class="accent-accent" bind:group={when} value="later" /> Later</label>
      <label class="flex items-center gap-2"><input type="radio" class="accent-accent" bind:group={when} value="now" /> Now</label>
      <label class="flex items-center gap-2">
        <input type="radio" class="accent-accent" bind:group={when} value="at" /> At
        <input type="time" class="input w-32" aria-label="Reboot time" bind:value={at} disabled={when !== "at"} />
      </label>
    </fieldset>
  {/snippet}
  {#snippet actions()}
    <button type="button" class="btn btn-primary" onclick={install}>Update</button>
  {/snippet}
</Modal>
