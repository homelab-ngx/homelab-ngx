<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, ProblemError, type Stack } from "#lib/api/client.ts";
  import { fetchChannels, keys } from "#lib/api/queries.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import { shortRev } from "#lib/format.ts";
  import { isNixpkgsCommit, nixpkgsLabel, runningNixpkgs } from "#lib/stacks.ts";

  interface Props {
    stack: Stack;
  }
  let { stack }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  const COMMIT = "commit";

  const channels = createQuery(() => ({ queryKey: keys.channels, queryFn: fetchChannels, staleTime: 3_600_000 }));
  const recommended = $derived(stack.approved?.manifest.nixpkgs ?? "");
  const running = $derived(runningNixpkgs(stack));
  const choice = $derived(stack.nixpkgs ?? "");

  /** The picker's value: "" (the recommendation), a channel, or COMMIT; null until changed. */
  let picked = $state<string | null>(null);
  let commit = $state("");
  let pending = $state(false);
  let error = $state("");
  /** The choice the daemon called older than what runs, and the operator's consent to it. */
  let downgrade = $state<string | null>(null);
  let consent = $state(false);

  const current = $derived(isNixpkgsCommit(choice) ? COMMIT : choice);
  const selected = $derived(picked ?? current);
  const wanted = $derived(selected === COMMIT ? commit.trim() || (isNixpkgsCommit(choice) ? choice : "") : selected);
  const commitProblem = $derived(selected === COMMIT && commit.trim() !== "" && !isNixpkgsCommit(commit.trim()));
  const changed = $derived(wanted !== choice && !(selected === COMMIT && !isNixpkgsCommit(wanted)));
  const warned = $derived(downgrade !== null && downgrade === wanted);
  /** Channels to offer: the daemon's list, plus a chosen one it no longer lists. */
  const offered = $derived([...new Set([...(channels.data ?? []), ...(choice && !isNixpkgsCommit(choice) ? [choice] : [])])]);

  async function save(e: SubmitEvent) {
    e.preventDefault();
    if (!changed) return;
    pending = true;
    error = "";
    try {
      const body = warned && consent ? { nixpkgs: wanted, allow_downgrade: true } : { nixpkgs: wanted };
      const s = await call<Stack>(api.PATCH("/stacks/{name}", { params: { path: { name: stack.name } }, body }));
      client.setQueryData([...keys.stacks, stack.name], s);
      void client.invalidateQueries({ queryKey: keys.stacks });
      picked = null;
      commit = "";
      downgrade = null;
      consent = false;
    } catch (err) {
      if (err instanceof ProblemError && err.code === "nixpkgs_downgrade") {
        downgrade = wanted;
        consent = false;
      } else {
        error = describe(err);
      }
    } finally {
      pending = false;
    }
  }
</script>

<section class="card" aria-label="NixOS version">
  <Disclosure section label="NixOS version" summary={isNixpkgsCommit(choice) ? shortRev(choice) : nixpkgsLabel(choice || recommended)} remember="stack-nixpkgs">
    {#snippet help()}
      The NixOS packages this stack is built with. Unstable gets new versions first; a numbered release changes less and gets security
      fixes. When the version followed moves on, the stack shows an update. Going back to an older release can break a stack whose
      data a newer version already upgraded, such as a database.
    {/snippet}
  {#if (stack.approved?.manifest.version ?? 0) < 7}
    <p class="mt-2 text-muted">Set by the stack's repo. Choosing a version needs manifest version 7.</p>
  {:else}
    <form class="grid gap-2" onsubmit={save}>
      <select class="input" aria-label="NixOS version" value={selected} onchange={(e) => (picked = e.currentTarget.value)}>
        <option value="">Stack default ({nixpkgsLabel(recommended)})</option>
        {#each offered as c (c)}
          <option value={c}>{nixpkgsLabel(c)}</option>
        {/each}
        <option value={COMMIT}>Commit…</option>
      </select>
      {#if selected === COMMIT}
        <input
          class="input"
          aria-label="nixpkgs commit"
          aria-invalid={commitProblem}
          placeholder={isNixpkgsCommit(choice) ? choice : "nixpkgs commit, 40 characters"}
          autocomplete="off"
          spellcheck="false"
          bind:value={commit}
        />
        {#if commitProblem}<p class="text-xs text-warn">Needs the full 40-character hash.</p>{/if}
      {/if}
      {#if warned}
        <div role="alert" class="rounded-md border border-warn/40 bg-warn-soft p-3">
          <p class="font-medium text-warn">Older than the version running now.</p>
          <p class="mt-1">Data a newer version upgraded, such as a database, may not be readable. Back up first.</p>
          <label class="mt-2 flex items-start gap-2">
            <input type="checkbox" class="mt-0.5 accent-accent" bind:checked={consent} />
            <span>Go back anyway</span>
          </label>
        </div>
      {/if}
      {#if error}<p role="alert" class="text-xs text-bad">{error}</p>{/if}
      <div class="flex flex-wrap items-center gap-3">
        <button type="submit" class="btn btn-primary" disabled={pending || !changed || (warned && !consent)}>{pending ? "Applying…" : "Apply"}</button>
        <p class="text-xs text-muted">Applies on next update.</p>
      </div>
    </form>
    <dl class="mt-3 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1">
      <dt class="text-muted">Running</dt>
      <dd>{running ? shortRev(running) : "not built"}</dd>
      {#if stack.nixpkgs_rev && stack.nixpkgs_rev !== running}
        <dt class="text-muted">Next update</dt>
        <dd>{shortRev(stack.nixpkgs_rev)}</dd>
      {/if}
    </dl>
  {/if}
  </Disclosure>
</section>
