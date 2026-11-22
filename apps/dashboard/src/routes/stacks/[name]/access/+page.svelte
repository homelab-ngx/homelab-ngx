<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { page } from "$app/state";
  import { describe } from "#lib/api/client.ts";
  import { busy, busyPoll, fetchDraft, fetchDrafts, fetchStack, idlePoll, keys } from "#lib/api/queries.ts";
  import Help from "#lib/components/Help.svelte";
  import StackNixpkgs from "#lib/components/StackNixpkgs.svelte";
  import { shortRev, stamp } from "#lib/format.ts";
  import { describeSource, draftOf, grants, sourceHref, stackTitle } from "#lib/stacks.ts";

  const name = $derived(page.params.name ?? "");
  const stack = createQuery(() => ({
    queryKey: [...keys.stacks, name],
    queryFn: () => fetchStack(name),
    refetchInterval: (q) => (q.state.data && busy(q.state.data) ? busyPoll : idlePoll),
  }));
  /** The draft a stack runs from, for where a fork came from. */
  const draftName = $derived(stack.data ? draftOf(stack.data.source) : null);
  const draft = createQuery(() => ({
    queryKey: keys.draft(draftName ?? ""),
    queryFn: () => fetchDraft(draftName!),
    enabled: draftName !== null,
  }));
  /** Drafts forked from this stack. */
  const drafts = createQuery(() => ({ queryKey: keys.drafts, queryFn: fetchDrafts, enabled: !!stack.data }));
  const forks = $derived((drafts.data ?? []).filter((d) => stack.data && d.forked_from === stack.data.source));
</script>

<svelte:head><title>Access · {stack.data ? stackTitle(stack.data) : name} · Homelab-ngx</title></svelte:head>

{#snippet maybeLink(href: string | null, text: string)}
  {#if href}<a class="link" {href} target="_blank" rel="noreferrer">{text}</a>{:else}{text}{/if}
{/snippet}

{#if stack.error}
  <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(stack.error)}</p>
{:else if stack.data}
  {@const s = stack.data}
  {@const src = describeSource(s.source)}
  {@const href = sourceHref(s.source)}
  <h1 class="sr-only">{stackTitle(s)} access</h1>

  <section class="card" aria-labelledby="grants-h">
    <h2 id="grants-h" class="font-semibold">
      <Help label="Approved grants">
        What this stack may reach outside its own container: host folders, web addresses, ports, the GPU. Each was approved here.
        Anything not listed is blocked, including the LAN and Tailscale.
      </Help>
    </h2>
    <ul class="mt-2 divide-y divide-line">
      {#each grants(s.approved?.manifest) as g (g.kind + g.detail)}
        <li class="flex gap-3 py-1.5">
          <span class="label w-20 shrink-0 pt-0.5">{g.kind}</span>
          <span class="break-all">{g.detail}</span>
        </li>
      {:else}
        <li class="py-1.5 text-muted">None</li>
      {/each}
    </ul>
  </section>

  <section class="card mt-4" aria-labelledby="source-h">
    <h2 id="source-h" class="font-semibold">Source</h2>
    <dl class="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5">
      <dt class="text-muted">{src.local ? "Folder" : "Repo"}</dt>
      <dd class="break-all">{@render maybeLink(href.repo, src.repo)}</dd>
      <dt class="text-muted">Branch</dt>
      <dd class="break-all">{@render maybeLink(href.branch, src.branch)}</dd>
      <dt class="text-muted">Pinned</dt>
      <dd>{s.approved ? `${shortRev(s.approved.rev)} · ${stamp(s.approved.at)}` : "nothing approved"}</dd>
      {#if draft.data?.forked_from}
        <dt class="text-muted">Forked from</dt>
        <dd class="break-all">
          {@render maybeLink(sourceHref(draft.data.forked_from).repo, describeSource(draft.data.forked_from).repo)} · {shortRev(draft.data.forked_rev ?? "")}
        </dd>
      {/if}
      {#if forks.length}
        <dt class="text-muted">Copies</dt>
        <dd>
          <ul>
            {#each forks as f (f.name)}<li><a class="link" href="/stacks/drafts/{f.name}">{f.name}</a></li>{/each}
          </ul>
        </dd>
      {/if}
    </dl>
    {#if s.source_dirty}
      <p class="mt-3 text-warn">Uncommitted changes in the folder are not installed.</p>
    {/if}
  </section>

  <div class="mt-4"><StackNixpkgs stack={s} /></div>

{:else}
  <p class="text-muted">Loading…</p>
{/if}
