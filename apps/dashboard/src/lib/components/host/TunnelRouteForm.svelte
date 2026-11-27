<script lang="ts">
  import { untrack } from "svelte";
  import { api, call, describe, type Stack } from "#lib/api/client.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import { approvedStackTargets, inZone, isDashboardTarget, type HostSettings, type Route } from "#lib/host.ts";

  interface Props {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    tunnel: HostSettings["tunnel"];
    domain: HostSettings["domain"];
    stacks: Stack[] | undefined;
    editIndex: number | null;
    onSaved: (s: HostSettings) => void;
  }
  let { open, onOpenChange, tunnel, domain, stacks, editIndex, onSaved }: Props = $props();

  const OTHER = "__other__";
  const zone = $derived(tunnel.dns_managed && tunnel.zone ? tunnel.zone : "");
  const options = $derived(approvedStackTargets(stacks, domain));
  const existing = $derived(editIndex !== null ? tunnel.routes[editIndex] : undefined);

  function initialHost(r: Route | undefined, z: string): string {
    if (!r) return "";
    if (z && r.hostname.toLowerCase().endsWith(`.${z.toLowerCase()}`)) {
      return r.hostname.slice(0, -(z.length + 1));
    }
    return r.hostname;
  }

  const initialOption = untrack(() => {
    const t = existing?.target ?? "";
    if (!t) return options[0]?.target ?? OTHER;
    return options.some((o) => o.target === t || o.host === t) ? (options.find((o) => o.target === t || o.host === t)?.target ?? OTHER) : OTHER;
  });

  let hostInput = $state(untrack(() => initialHost(existing, zone)));
  let selectedTarget = $state(initialOption);
  let customTarget = $state(untrack(() => (initialOption === OTHER ? (existing?.target ?? "") : "")));
  let isPublic = $state(untrack(() => Boolean(existing?.public)));
  let path = $state(untrack(() => existing?.path ?? ""));
  let saving = $state(false);
  let error = $state("");

  const fullHostname = $derived.by(() => {
    const raw = hostInput.trim().toLowerCase().replace(/\.$/, "");
    if (!raw) return "";
    if (zone && !raw.includes(".")) return `${raw}.${zone}`;
    return raw;
  });
  const targetValue = $derived((selectedTarget === OTHER ? customTarget : selectedTarget).trim().toLowerCase());

  const problem = $derived.by(() => {
    if (!fullHostname) return "Enter a public name.";
    if (!fullHostname.includes(".")) return "Enter a full host name.";
    if (zone && !inZone(fullHostname, zone)) return `Not in ${zone}.`;
    if (!targetValue) return "Choose what this route serves.";
    if (isDashboardTarget(targetValue, domain)) return "The dashboard cannot be published through the tunnel.";
    const trimmedPath = path.trim();
    const dup = tunnel.routes.some(
      (r, idx) => idx !== editIndex && r.hostname.toLowerCase() === fullHostname && (r.path ?? "") === trimmedPath,
    );
    if (dup) return "That route is already listed.";
    return "";
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (problem || saving) return;
    saving = true;
    error = "";
    const nextRoute: Route = {
      hostname: fullHostname,
      target: targetValue,
      ...(path.trim() ? { path: path.trim() } : {}),
      ...(isPublic ? { public: true } : {}),
    };
    const routes = tunnel.routes.map((r) => ({
      hostname: r.hostname,
      target: r.target,
      ...(r.path ? { path: r.path } : {}),
      ...(r.public ? { public: true } : {}),
    }));
    if (editIndex === null) routes.push(nextRoute);
    else routes[editIndex] = nextRoute;
    try {
      const saved = await call<HostSettings>(api.PUT("/host-settings/tunnel", { body: { enabled: tunnel.enabled, routes } }));
      onSaved(saved);
      onOpenChange(false);
    } catch (err) {
      error = describe(err);
    } finally {
      saving = false;
    }
  }
</script>

<Modal {open} {onOpenChange} title={editIndex === null ? "Add a route" : "Edit route"}>
  <form id="tunnel-route-form" onsubmit={submit} class="space-y-4">
    <div>
      <label class="mb-1 block text-muted" for="tunnel-route-host">Public name</label>
      {#if zone}
        <div class="flex items-center gap-1.5">
          <input
            id="tunnel-route-host"
            class="input min-w-0 flex-1"
            placeholder="photos"
            autocomplete="off"
            bind:value={hostInput}
          />
          <span class="shrink-0 text-muted">.{zone}</span>
        </div>
      {:else}
        <input
          id="tunnel-route-host"
          class="input w-full"
          placeholder="photos.example.net"
          autocomplete="off"
          bind:value={hostInput}
        />
      {/if}
    </div>

    <div>
      <div class="mb-1 text-muted">
        <Help label="Serves">Private host name Caddy serves for this route.</Help>
      </div>
      {#if options.length > 0}
        <select aria-label="Serves" class="input w-full" bind:value={selectedTarget}>
          {#each options as opt (opt.host)}
            <option value={opt.target}>{opt.stackTitle} — {opt.host}</option>
          {/each}
          <option value={OTHER}>Other host name…</option>
        </select>
      {/if}
      {#if selectedTarget === OTHER || options.length === 0}
        <input
          aria-label="Private host name"
          class="input w-full {options.length > 0 ? 'mt-2' : ''}"
          placeholder={domain.domain ? `photos` : "photos.home.example"}
          autocomplete="off"
          bind:value={customTarget}
        />
      {/if}
    </div>

    <div class="flex flex-wrap items-center gap-x-2">
      <input type="checkbox" class="accent-accent" aria-label="Open to anyone" bind:checked={isPublic} />
      <Help label="Open to anyone">Allows visitors without Cloudflare Access and clears the open warning.</Help>
    </div>

    <Disclosure label="Advanced" open={Boolean(path)}>
      <label class="mb-1 block text-muted" for="tunnel-route-path">Path (optional)</label>
      <input
        id="tunnel-route-path"
        class="input w-full"
        placeholder="every path"
        autocomplete="off"
        bind:value={path}
      />
    </Disclosure>

    {#if error}
      <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
    {:else if problem}
      <p class="text-muted">{problem}</p>
    {/if}
  </form>

  {#snippet actions()}
    <button
      type="submit"
      form="tunnel-route-form"
      class="btn btn-primary"
      disabled={saving || Boolean(problem)}
      title={problem || undefined}
    >
      {saving ? "Saving…" : editIndex === null ? "Add route" : "Save"}
    </button>
  {/snippet}
</Modal>
