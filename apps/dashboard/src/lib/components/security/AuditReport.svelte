<script lang="ts">
  import type { Finding, SecurityAudit } from "#lib/api/client.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { usageLabel } from "#lib/assistant.ts";
  import { stamp } from "#lib/format.ts";
  import { findingsById, POSTURE, RESULT, SEVERITY } from "#lib/security.ts";
  import FindingDialog from "./FindingDialog.svelte";

  interface Props {
    audit: SecurityAudit;
  }
  let { audit }: Props = $props();

  const byId = $derived(findingsById(audit));
  const first = $derived(audit.first.map((id) => byId.get(id)).filter((f): f is Finding => !!f));
  const areas = $derived(audit.chunks.filter((c) => c.area !== "summary"));
  let open = $state<Finding | null>(null);
</script>

{#snippet row(f: Finding)}
  <li class="grid grid-cols-[5.5rem_1fr] items-baseline gap-3 py-1.5">
    <Pill tone={SEVERITY[f.severity]}>{f.severity}</Pill>
    <span>
      <button type="button" class="link text-left" onclick={() => (open = f)}>{f.title}</button>
      {#if !f.verified}<span class="ml-1 text-xs text-warn">unverified</span>{/if}
    </span>
  </li>
{/snippet}

{#if audit.result !== "running"}
  <section class="card" aria-labelledby="report-h">
    <div class="flex flex-wrap items-center gap-2">
      <h2 id="report-h" class="font-semibold">
        <Help label="Report">
          Each finding quotes the line it rests on. Unverified: that line is not in what the model was given.
        </Help>
      </h2>
      {#if audit.posture}<Pill tone={POSTURE[audit.posture]}>{audit.posture}</Pill>
      {:else}<Pill tone={RESULT[audit.result].tone}>{RESULT[audit.result].label}</Pill>{/if}
    </div>
    <p class="mt-1 text-xs text-muted">{[stamp(audit.started_at), audit.model, usageLabel(audit.usage)].filter(Boolean).join(" · ")}</p>
    {#if audit.summary}<p class="mt-3">{audit.summary}</p>{/if}
    {#if audit.error}<p role="alert" class="mt-3 text-bad">{audit.error}</p>{/if}
    {#if first.length}
      <h3 class="label mt-4">Fix first</h3>
      <ol class="mt-1 divide-y divide-line">
        {#each first as f (f.id)}{@render row(f)}{/each}
      </ol>
    {/if}
  </section>
{/if}

{#each areas as c (c.area)}
  <section class="card" aria-labelledby="area-{c.area}">
    <div class="flex flex-wrap items-center gap-2">
      <h2 id="area-{c.area}" class="font-semibold">{c.title}</h2>
      {#if c.state === "failed"}<Pill tone="bad">failed</Pill>
      {:else if c.state !== "done"}<Pill tone="info">{c.state}</Pill>
      {:else if c.findings.length === 0}<Pill tone="ok">clear</Pill>{/if}
    </div>
    {#if c.state === "failed"}
      <p class="mt-2 break-words text-bad">{c.error}</p>
    {/if}
    {#if c.findings.length}
      <ul class="mt-2 divide-y divide-line">
        {#each c.findings as f (f.id)}{@render row(f)}{/each}
      </ul>
    {/if}
    {#if c.strengths.length}
      <Disclosure label="Done well" class="mt-3">
        <ul class="list-disc pl-5">
          {#each c.strengths as s, i (i)}<li>{s}</li>{/each}
        </ul>
      </Disclosure>
    {/if}
    {#if c.raw}
      <Disclosure label="Answer" class="mt-3">
        <pre class="max-h-80 overflow-auto font-mono text-xs whitespace-pre-wrap">{c.raw}</pre>
      </Disclosure>
    {/if}
    <Disclosure label="Sent" class="mt-3">
      <pre class="max-h-96 overflow-auto font-mono text-xs whitespace-pre-wrap">{c.material}</pre>
    </Disclosure>
  </section>
{/each}

{#if open}<FindingDialog finding={open} onClose={() => (open = null)} />{/if}
