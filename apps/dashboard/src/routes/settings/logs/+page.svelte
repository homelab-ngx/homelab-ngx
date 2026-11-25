<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { page } from "$app/state";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import LogViewer, { type LogUnit } from "#lib/components/LogViewer.svelte";

  type LogViewerConfig = Schema<"LogViewerConfig">;

  const DEFAULT_UNIT = "homelab-daemon";
  // Stack services have their logs on their stack's page.
  const GROUPS: Record<string, string> = { core: "Core", backup: "Backups" };

  const config = createQuery(() => ({
    queryKey: ["daemon", "log-viewer-config"],
    queryFn: () => call<LogViewerConfig>(api.GET("/log-viewer-config")),
    staleTime: 60_000,
  }));
  const units = $derived<LogUnit[]>(
    (config.data?.units ?? []).filter((u) => u.group in GROUPS).map((u) => ({ id: u.id, label: u.label, group: GROUPS[u.group] })),
  );
  const initial = page.url.searchParams.get("unit") || DEFAULT_UNIT;
</script>

<svelte:head><title>Logs · Settings · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/settings">Settings</a> /</p>
<h1 class="mt-1 text-xl font-semibold">
  <Help label="Logs">Log lines of the host and of backup jobs, newest first. A stack's services have their logs on the stack's page.</Help>
</h1>

<div class="mt-4">
  {#if config.error}
    <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(config.error)}</p>
  {:else if config.data}
    <LogViewer {units} {initial} />
  {:else}
    <p class="text-muted">Loading…</p>
  {/if}
</div>
