<script lang="ts">
  import { untrack, type Snippet } from "svelte";
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import StatusMark from "#lib/components/StatusMark.svelte";
  import Switch from "#lib/components/Switch.svelte";
  import { ago } from "#lib/format.ts";
  import { fetchPower, powerHref, powerKey } from "#lib/power.ts";
  import { startInstall } from "#lib/power-start.svelte.ts";
  import { availableRelease, type CoreDeploy, headline, installedSummary, runningVersion, updateOf, VERSION_ROLE } from "./core.ts";

  interface Props {
    image: Schema<"ImageStatus">;
    /** An operation is running. */
    busy: boolean;
    onReboot: () => void;
    /** Called when an operation (check, build, update) returns its deploy record. */
    onStarted?: (d: CoreDeploy) => void;
    changelog?: Snippet;
  }
  let { image, busy, onReboot, onStarted, changelog }: Props = $props();
  const id = $props.id();

  const client = useQueryClient();
  let error = $state("");
  let checking = $state(false);
  let installing = $state(false);
  let when = $state<"later" | "now" | "at">("later");
  let at = $state("03:30");

  const power = createQuery(() => ({ queryKey: powerKey, queryFn: fetchPower, enabled: installing, staleTime: 0 }));
  const busyWork = $derived(power.data?.busy ?? []);
  const interrupts = $derived(when === "now" && busyWork.length > 0);

  const op = createMutation(() => ({
    mutationFn: (run: () => Promise<unknown>) => run(),
    onMutate: () => (error = ""),
    onError: (e: unknown) => (error = describe(e)),
    onSettled: () => {
      checking = false;
      client.invalidateQueries({ queryKey: ["daemon", "system-updates"] });
    },
  }));

  function runDeploy(fn: () => Promise<CoreDeploy>) {
    op.mutate(async () => {
      const d = await fn();
      onStarted?.(d);
      return d;
    });
  }

  const build = () => runDeploy(() => call<CoreDeploy>(api.POST("/system-updates/image/build")));
  const download = () => {
    checking = true;
    runDeploy(() => call<CoreDeploy>(api.POST("/system-updates/image/download")));
  };

  // One operation installs what was downloaded: the image, then the core.
  // Only a new image has a reboot to choose.
  const update = $derived(updateOf(image));
  const release = $derived(availableRelease(image));
  const head = $derived(headline(image));
  async function install() {
    installing = false;
    const reboot = !update?.reboot || when === "later" ? "" : when === "now" ? "now" : at;
    // Updating and rebooting now is one run, followed on the power page
    // from the start. It needs the boot the run begins in.
    if (reboot === "now" && update) {
      const version = update.version;
      const from = (await client.ensureQueryData({ queryKey: powerKey, queryFn: fetchPower }).catch(() => undefined))?.boot_id;
      if (from) {
        startInstall(from);
        await goto(powerHref("reboot", from, page.url.pathname, { to: version, install: true, t: Date.now() }));
        return;
      }
    }
    runDeploy(() => call<CoreDeploy>(api.POST("/system-updates/image/release", { body: { reboot } })));
  }
  const boot = (version: string, mode: "once" | "default") => op.mutate(() => call(api.POST("/system-updates/image/boot", { body: { version, mode } })));
  const bootMenu = (version: string) => [
    { label: "Boot once", onSelect: () => boot(version, "once") },
    { label: "Boot by default", onSelect: () => boot(version, "default") },
  ];

  // The time a release installs by itself: a draft of the stored one, saved
  // when the switch or time changes.
  let auto = $state(untrack(() => image.auto_install_at !== undefined && image.auto_install_at !== ""));
  let autoAt = $state(untrack(() => image.auto_install_at || "03:30"));
  const saveAuto = () => op.mutate(() => call(api.PUT("/system-updates/image/auto-install", { body: { at: auto ? autoAt : "" } })));
  function toggleAuto(on: boolean) {
    auto = on;
    saveAuto();
  }

  // The version it fell back from is said once, in its own line.
  const versions = $derived(image.installed.filter((v) => v.version !== image.fell_back_from));
  const slotSummary = $derived(installedSummary(image));
  // A core updated on its own runs a version the image does not have.
  const coreApart = $derived(image.core.active && image.core.active !== image.booted ? image.core.active : "");
</script>

<section class="card mt-5 border-core/40" aria-labelledby="{id}-h">
  <h2 id="{id}-h" class="font-semibold text-core">
    <Help label="Version">
      Downloads and verifies updates before installing beside the running image. Failed boots return to the fallback automatically.
    </Help>
  </h2>

  <div class="mt-2 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
    <p class="flex flex-wrap items-center gap-x-2">
      <Pill tone={head.tone}>{head.state}</Pill>
      {#if head.detail}<span class="text-muted">{head.detail}</span>{/if}
    </p>
    <div class="flex flex-wrap gap-2">
      {#if release}
        <button type="button" class="btn btn-primary" disabled={busy || op.isPending} onclick={download}>
          {checking ? "Downloading…" : `Download ${release}`}
        </button>
      {:else}
        <button type="button" class="btn" disabled={busy || op.isPending} onclick={download}>
          {checking ? "Checking…" : "Check now"}
        </button>
      {/if}
      {#if image.pending_reboot}
        <button type="button" class="btn btn-primary" disabled={busy} onclick={onReboot}>Reboot…</button>
      {:else if update}
        <button type="button" class="btn btn-primary" disabled={busy || op.isPending} onclick={() => (update.reboot ? (installing = true) : install())}>
          Update to {update.version}{update.reboot ? "…" : ""}
        </button>
      {/if}
    </div>
  </div>

  <dl class="mt-4 grid grid-cols-2 gap-3 sm:grid-cols-3">
    <div>
      <dt class="label">Running</dt>
      <dd class="mt-0.5 tabular-nums">{runningVersion(image) || "–"}</dd>
    </div>
    <div>
      <dt class="label">Newest release</dt>
      <dd class="mt-0.5 tabular-nums">{image.release.latest || "–"}</dd>
    </div>
    <div>
      <dt class="label">Checked</dt>
      <dd class="mt-0.5 whitespace-nowrap">{image.release.checked_at ? ago(image.release.checked_at) : "–"}</dd>
    </div>
  </dl>
  {#if image.release.error}<p class="mt-2 text-muted">Check failed: {image.release.error}</p>{/if}
  {#if image.boot_check}
    <p role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">
      <Help label="Start check failed">This version ran before, so the host stayed up. Fix the cause or pick another version under Installed images.</Help>: {image.boot_check}
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

  <div class="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-line pt-3">
    <Help label="Update automatically">Installs downloaded releases at this time of day and reboots when needed. Off: manual only.</Help>
    <div class="flex items-center gap-2.5">
      <input
        type="time"
        class="input w-32"
        aria-label="Time of the automatic update"
        bind:value={autoAt}
        onchange={saveAuto}
        disabled={!auto || op.isPending}
      />
      <Switch checked={auto} label="Update automatically" disabled={op.isPending} onChange={toggleAuto} />
    </div>
  </div>
</section>

{@render changelog?.()}

<section class="card mt-4">
  <Disclosure section label="Installed images" summary={slotSummary || undefined} remember="updates-versions">
    {#snippet help()}Keeps the running image and one fallback image. Boots the fallback automatically if an update fails.{/snippet}
    <ul class="flex flex-col divide-y divide-line">
      {#each versions as v (v.version)}
        {@const role = VERSION_ROLE[v.state] ?? { tone: "idle" as const, label: v.state }}
        <li class="flex min-h-10 items-center gap-x-3 py-1.5">
          <span class="flex min-w-0 grow flex-wrap items-center gap-x-2 gap-y-0.5">
            <StatusMark tone={role.tone} />
            <span>{role.label}</span>
            {#if image.oneshot === v.version}<span class="text-xs text-muted">· boots once</span>{/if}
            {#if image.default === v.version}<span class="text-xs text-muted">· boots by default</span>{/if}
          </span>
          <span class="shrink-0 tabular-nums text-muted">{v.version}</span>
          <span class="flex w-8 shrink-0 justify-end">
            {#if v.version !== image.booted}<RowMenu name={v.version} items={bootMenu(v.version)} />{/if}
          </span>
        </li>
      {/each}
      {#if coreApart}
        <li class="flex min-h-10 items-center gap-x-3 py-1.5">
          <span class="flex min-w-0 grow items-center gap-x-2">
            <StatusMark tone="ok" />
            <span>Dashboard</span>
          </span>
          <span class="shrink-0 tabular-nums text-muted">{coreApart}</span>
          <span class="w-8 shrink-0"></span>
        </li>
      {/if}
    </ul>
    {#if image.default}
      <div class="mt-2 flex justify-end">
        <button type="button" class="btn btn-sm" disabled={busy || op.isPending} onclick={() => boot("", "default")}>Boot newest by default</button>
      </div>
    {/if}
  </Disclosure>
</section>

{#if image.source}
  <section class="card mt-4">
    <Disclosure section label="Development" summary={image.source} remember="updates-development">
      {#snippet help()}Builds main on this host when newer commits exist and prepares it as an update.{/snippet}
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
    {#if interrupts}
      <div role="status" class="mt-3 rounded-md bg-warn-soft px-3 py-2 text-warn">
        <p>Interrupted:</p>
        <ul class="mt-1 list-inside list-disc">
          {#each busyWork as b (b)}<li>{b}</li>{/each}
        </ul>
      </div>
    {/if}
  {/snippet}
  {#snippet actions()}
    <button type="button" class="btn {interrupts ? 'btn-danger' : 'btn-primary'}" onclick={install}>{interrupts ? "Update anyway" : "Update"}</button>
  {/snippet}
</Modal>
