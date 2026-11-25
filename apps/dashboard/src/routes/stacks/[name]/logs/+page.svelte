<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { page } from "$app/state";
  import { describe } from "#lib/api/client.ts";
  import { fetchServices, idlePoll, keys } from "#lib/api/queries.ts";
  import LogViewer from "#lib/components/LogViewer.svelte";
  import { logUnits, servicesOf } from "#lib/stacks.ts";

  const name = $derived(page.params.name ?? "");
  const services = createQuery(() => ({ queryKey: keys.services, queryFn: fetchServices, refetchInterval: idlePoll }));
  const units = $derived(logUnits(name, servicesOf(services.data, name)));
  /** The service to show first: ?unit=<log id>, else the first one. */
  const initial = $derived(page.url.searchParams.get("unit") || units[0]?.id || "");
</script>

<svelte:head><title>Logs · {name} · Homelab-ngx</title></svelte:head>

<h1 class="sr-only">{name} logs</h1>
{#if services.error}
  <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(services.error)}</p>
{:else if services.data && units.length === 0}
  <p class="rounded-lg border border-line bg-surface px-4 py-3 text-muted">No services listed.</p>
{:else if services.data}
  {#key name}
    <LogViewer {units} {initial} />
  {/key}
{:else}
  <p class="text-muted">Loading…</p>
{/if}
