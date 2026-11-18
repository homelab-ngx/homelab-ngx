<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type SecurityAudit } from "#lib/api/client.ts";
  import { busyPoll, fetchAssistant, idlePoll, keys } from "#lib/api/queries.ts";
  import Help from "#lib/components/Help.svelte";
  import AuditProgress from "#lib/components/security/AuditProgress.svelte";
  import AuditReport from "#lib/components/security/AuditReport.svelte";
  import AuditsDialog from "#lib/components/security/AuditsDialog.svelte";
  import { fetchAudit, fetchAudits, securityKeys } from "#lib/security.ts";

  const client = useQueryClient();
  const assistant = createQuery(() => ({ queryKey: keys.assistant, queryFn: fetchAssistant }));
  const assistantOn = $derived(!!assistant.data && assistant.data.provider !== "none");

  const audits = createQuery(() => ({
    queryKey: securityKeys.audits,
    queryFn: fetchAudits,
    refetchInterval: (q) => (q.state.data?.some((a) => a.result === "running") ? busyPoll : idlePoll),
  }));
  const running = $derived(audits.data?.some((a) => a.result === "running") ?? false);

  /** The audit shown: one picked from the past ones, else the newest. */
  let picked = $state<number | null>(null);
  const shownId = $derived(picked ?? audits.data?.[0]?.id ?? null);
  const shown = createQuery(() => ({
    queryKey: securityKeys.audit(shownId ?? 0),
    queryFn: () => fetchAudit(shownId!),
    enabled: shownId !== null,
    refetchInterval: (q) => (q.state.data?.result === "running" ? busyPoll : false),
  }));
  let past = $state(false);

  const start = createMutation(() => ({
    mutationFn: () => call<SecurityAudit>(api.POST("/security/audits")),
    onSuccess: (a: SecurityAudit) => {
      picked = null;
      client.setQueryData(securityKeys.audit(a.id), a);
    },
    onSettled: () => client.invalidateQueries({ queryKey: securityKeys.audits }),
  }));
  const blocked = $derived(!assistantOn ? "Set up the assistant first." : running ? "An audit is running." : "");
</script>

<svelte:head><title>Security audit · Settings · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/settings">Settings</a> /</p>
<h1 class="mt-1 text-xl font-semibold">
  <Help label="Security audit">
    The assistant's audit model reviews what the daemon knows about the host, one area at a time: exposure, access, stacks,
    secrets and backups, updates. Secret values are removed first. It suggests; it changes nothing.
  </Help>
</h1>

<div class="mt-4 flex flex-wrap items-center gap-2">
  <button type="button" class="btn btn-primary" disabled={!!blocked || start.isPending} title={blocked || undefined} onclick={() => start.mutate()}>
    {start.isPending ? "Gathering…" : "Run audit"}
  </button>
  {#if (audits.data?.length ?? 0) > 1}
    <button type="button" class="btn" onclick={() => (past = true)}>Past audits</button>
  {/if}
  {#if !assistantOn && assistant.data}<a class="link text-xs" href="/settings/assistant">Set up the assistant</a>{/if}
</div>

<div class="mt-5 grid max-w-3xl gap-4">
  {#if start.error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(start.error)}</p>{/if}
  {#if audits.error}
    <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(audits.error)}</p>
  {:else if audits.data && audits.data.length === 0}
    <p class="text-muted">No audit yet.</p>
  {/if}
  {#if shown.data}
    {#if shown.data.result === "running"}<AuditProgress audit={shown.data} />{/if}
    {#if shown.data.result !== "running" || shown.data.progress.done > 0}<AuditReport audit={shown.data} />{/if}
  {:else if shown.error}
    <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(shown.error)}</p>
  {/if}
</div>

{#if past && audits.data}
  <AuditsDialog
    audits={audits.data}
    onSelect={(id) => {
      picked = id;
      past = false;
    }}
    onClose={() => (past = false)}
  />
{/if}
