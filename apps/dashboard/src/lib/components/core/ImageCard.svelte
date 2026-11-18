<script lang="ts">
  import { createMutation, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill, { type Tone } from "#lib/components/Pill.svelte";
  import { shortRev } from "#lib/format.ts";

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
  const updateCore = () => op.mutate(() => call(api.POST("/system-updates/image/core")));
  function install() {
    installing = false;
    const reboot = when === "now" ? "now" : when === "at" ? at : "";
    op.mutate(() => call(api.POST("/system-updates/image/install", { body: { reboot } })));
  }

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
  </dl>

  <ul class="mt-3 flex flex-col gap-1.5 border-t border-line pt-3">
    {#if image.fell_back_from}
      <li class="flex flex-wrap items-center gap-x-2"><Pill tone="warn">fell back</Pill>{image.fell_back_from} did not boot</li>
    {/if}
    {#each others as v (v.version)}
      <li class="flex flex-wrap items-center gap-x-2">
        <Pill tone={STATE[v.state] ?? "idle"}>{v.state === "pending" ? "next boot" : v.state}</Pill>image {v.version}
      </li>
    {/each}
    {#each image.available as v (v)}<li class="flex items-center gap-x-2"><Pill tone="info">staged</Pill>image {v}</li>{/each}
    {#each image.core.available as v (v)}<li class="flex items-center gap-x-2"><Pill tone="info">staged</Pill>core {v}</li>{/each}
  </ul>

  {#if error}<p role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}

  <div class="mt-4 flex flex-wrap justify-end gap-2">
    <button type="button" class="btn" disabled={busy || op.isPending || !image.source} title={image.source ? "" : "No source repository set"} onclick={build}>
      Build from main
    </button>
    <button type="button" class="btn {image.core.available.length ? 'btn-primary' : ''}" disabled={busy || op.isPending || !image.core.available.length} onclick={updateCore}>
      Update core
    </button>
    <button type="button" class="btn {image.available.length ? 'btn-primary' : ''}" disabled={busy || op.isPending || !image.available.length} onclick={() => (installing = true)}>
      Install image…
    </button>
    {#if image.pending_reboot}<button type="button" class="btn" disabled={busy} onclick={onReboot}>Reboot…</button>{/if}
  </div>
</section>

<Modal open={installing} onOpenChange={(o) => (installing = o)} title="Install image {image.available[0] ?? ''}?" description="It goes in the free slot and boots at a reboot.">
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
