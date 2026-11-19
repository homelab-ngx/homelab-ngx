<script lang="ts">
  import { untrack } from "svelte";
  import { createMutation, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill, { type Tone } from "#lib/components/Pill.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import { ago } from "#lib/format.ts";

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
  // One operation installs what is staged: the image, then the core. Only a
  // new image has a reboot to choose.
  const staged = $derived(image.available[0] ?? image.core.available[0] ?? "");
  function install() {
    installing = false;
    const reboot = !image.available.length || when === "later" ? "" : when === "now" ? "now" : at;
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

  const STATE: Record<string, Tone> = { booted: "ok", good: "idle", pending: "info", bad: "bad" };
  // The version it fell back from is said once, in its own line.
  const others = $derived(image.installed.filter((v) => v.version !== image.booted && v.version !== image.fell_back_from));
</script>

<section class="card mt-5 border-core/40" aria-labelledby="{id}-h">
  <h2 id="{id}-h" class="font-semibold text-core">
    <Help label="Image">
      The host's system is one read-only image in two slots. A new version goes in the free slot and boots at the next reboot; one
      that does not come up falls back to the slot before. The core (daemon, dashboard, Caddy) updates on its own, without a reboot.
    </Help>
  </h2>

  <dl class="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-3">
    <div>
      <dt class="label">Image</dt>
      <dd class="mt-0.5">{image.booted || "–"}</dd>
    </div>
    <div>
      <dt class="label">Core</dt>
      <dd class="mt-0.5">{image.core.active || "–"}</dd>
    </div>
    <div>
      <dt class="label">Source</dt>
      <dd class="mt-0.5 break-all">{image.source || "–"}</dd>
    </div>
    <div>
      <dt class="label">Newest release</dt>
      <dd class="mt-0.5">
        {image.release.latest || "–"}
        {#if image.release.checked_at}<span class="text-muted">· checked {ago(image.release.checked_at)}</span>{/if}
      </dd>
    </div>
  </dl>
  {#if image.release.error}<p class="mt-2 text-muted">Release check failed: {image.release.error}</p>{/if}
  {#if image.boot_check}
    <p role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">
      <Help label="Boot check failed">This version ran before, so the host stayed up instead of restarting. Repair the cause, or boot another version below.</Help>: {image.boot_check}
    </p>
  {/if}

  <ul class="mt-3 flex flex-col gap-1.5 border-t border-line pt-3">
    {#if image.fell_back_from}
      <li class="flex flex-wrap items-center gap-x-2">
        <Pill tone="warn">fell back</Pill><span class="grow">{image.fell_back_from} did not boot</span>
        <RowMenu name="image {image.fell_back_from}" items={bootMenu(image.fell_back_from)} />
      </li>
    {/if}
    {#if image.core.fell_back_from}
      <li class="flex flex-wrap items-center gap-x-2"><Pill tone="warn">fell back</Pill>core {image.core.fell_back_from} did not start</li>
    {/if}
    {#each others as v (v.version)}
      <li class="flex flex-wrap items-center gap-x-2">
        <Pill tone={STATE[v.state] ?? "idle"}>{v.state === "pending" ? "next boot" : v.state}</Pill>
        <span class="grow">
          image {v.version}
          {#if image.oneshot === v.version}<span class="text-muted">· boots once</span>{/if}
        </span>
        <RowMenu name="image {v.version}" items={bootMenu(v.version)} />
      </li>
    {/each}
    {#if image.default}
      <li class="flex flex-wrap items-center gap-x-2">
        <Pill tone="info">default</Pill><span class="grow">image {image.default}</span>
        <button type="button" class="btn btn-sm" disabled={busy || op.isPending} onclick={() => boot("", "default")}>Use newest</button>
      </li>
    {/if}
    {#each image.available as v (v)}<li class="flex items-center gap-x-2"><Pill tone="info">staged</Pill>image {v}</li>{/each}
    {#each image.core.available as v (v)}<li class="flex items-center gap-x-2"><Pill tone="info">staged</Pill>core {v}</li>{/each}
  </ul>

  {#if error}<p role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}

  <div class="mt-3 flex flex-wrap items-center gap-2 border-t border-line pt-3">
    <label class="flex items-center gap-2">
      <input type="checkbox" class="accent-accent" bind:checked={auto} onchange={saveAuto} disabled={op.isPending} />
      <Help label="Install by itself at">A downloaded release installs at this time of day, and the host reboots when it brings a new image. Off: only when you install it here.</Help>
    </label>
    <input type="time" class="input w-32" aria-label="Install time" bind:value={autoAt} onchange={saveAuto} disabled={!auto || op.isPending} />
  </div>

  <div class="mt-4 flex flex-wrap justify-end gap-2">
    <button type="button" class="btn" disabled={busy || op.isPending} onclick={download}>Check for updates</button>
    <button type="button" class="btn" disabled={busy || op.isPending || !image.source} title={image.source ? "" : "No source repository set"} onclick={build}>
      Build from main
    </button>
    <button type="button" class="btn {staged ? 'btn-primary' : ''}" disabled={busy || op.isPending || !staged} onclick={() => (image.available.length ? (installing = true) : install())}>
      {image.available.length ? "Install…" : "Install"}
    </button>
    {#if image.pending_reboot || image.oneshot || (image.default && image.default !== image.booted)}
      <button type="button" class="btn" disabled={busy} onclick={onReboot}>Reboot…</button>
    {/if}
  </div>
</section>

<Modal open={installing} onOpenChange={(o) => (installing = o)} title="Install {staged}?" description="The image goes in the free slot and boots at a reboot. The core updates now.">
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
    <button type="button" class="btn btn-primary" onclick={install}>Install</button>
  {/snippet}
</Modal>
