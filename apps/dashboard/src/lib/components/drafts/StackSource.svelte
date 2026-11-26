<script lang="ts">
  import { useQueryClient } from "@tanstack/svelte-query";
  import { untrack } from "svelte";
  import { beforeNavigate, goto } from "$app/navigation";
  import { api, call, describe, type Draft, type Schema, type Stack } from "#lib/api/client.ts";
  import { keys } from "#lib/api/queries.ts";
  import CodeEditor from "#lib/components/CodeEditor.svelte";
  import ForkStack from "#lib/components/ForkStack.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import { bufferOf, changesOf, MANIFEST, type Buffer } from "#lib/drafts.ts";
  import { shortRev } from "#lib/format.ts";
  import { describeSource, sourceHref, stackTitle } from "#lib/stacks.ts";
  import FileList from "./FileList.svelte";

  type StackFiles = Schema<"StackFiles">;

  interface Props {
    stack: Stack;
    /** The files of the commit the stack runs, or of the fetched one before any is approved. */
    files: StackFiles;
    /** The fork was made but the edits were not saved into it: the editor that takes over starts from them. */
    onUnsaved?: (buffer: Buffer, error: string) => void;
  }
  let { stack, files, onUnsaved }: Props = $props();

  const client = useQueryClient();

  /** The files as typed: they can be changed here, not saved. */
  let buffer = $state<Buffer>(bufferOf(untrack(() => files.files)));
  let open = $state(MANIFEST);
  let error = $state("");
  let working = $state(false);
  let confirming = $state(false);
  let copying = $state(false);

  const changes = $derived(changesOf(files.files, buffer));
  const changed = $derived(changes.map((c) => c.path));
  const dirty = $derived(changes.length > 0);
  const src = $derived(describeSource(stack.source));
  const repoHref = $derived(sourceHref(stack.source).repo);
  const title = $derived(stackTitle(stack));

  /**
   * Moves the stack onto a copy on this host (a draft), then saves what was
   * typed into it. The page then shows the copy, editable.
   */
  async function fork(save: boolean) {
    error = "";
    working = true;
    let d: Draft;
    try {
      d = await call<Draft>(api.POST("/stacks/{name}/fork", { params: { path: { name: stack.name } } }));
    } catch (e) {
      error = describe(e);
      working = false;
      return;
    }
    if (save && dirty) {
      try {
        d = await call<Draft>(api.PUT("/drafts/{name}", { params: { path: { name: d.name } }, body: { changes } }));
      } catch (e) {
        onUnsaved?.($state.snapshot(buffer), `Forked, but your changes are not saved yet: ${describe(e)}`);
      }
    }
    client.setQueryData(keys.draft(d.name), d);
    buffer = bufferOf(files.files); // kept by the copy now, or handed on above
    confirming = false;
    working = false;
    await client.invalidateQueries({ queryKey: keys.all });
  }

  let pendingNav = $state<string | null>(null);
  let allowLeave = false;

  beforeNavigate((nav) => {
    if (allowLeave || !dirty || nav.type === "leave" || !nav.to) return;
    nav.cancel();
    pendingNav = `${nav.to.url.pathname}${nav.to.url.search}${nav.to.url.hash}`;
  });

  function confirmLeave() {
    const dest = pendingNav;
    pendingNav = null;
    if (!dest) return;
    allowLeave = true;
    void goto(dest).finally(() => {
      allowLeave = false;
    });
  }
</script>

<svelte:window
  onbeforeunload={(e) => {
    if (dirty) e.preventDefault();
  }}
/>

<!-- A stack's files read from the commit it runs (read
     only). Typing is allowed; the banner says it cannot be saved here. -->
<div class="flex flex-col overflow-hidden rounded-lg border border-line bg-surface xl:h-[calc(100dvh-8.5rem)] xl:min-h-[36rem]">
  <div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-line bg-sunken px-3 py-1.5">
    <h1 class="sr-only">{title} source code</h1>
    <p class="min-w-0 flex-1 truncate text-muted">
      Read only ·
      {#if repoHref}<a class="link" href={repoHref} target="_blank" rel="noreferrer">{src.repo}</a>{:else}{src.repo}{/if}
      · {shortRev(files.rev)}
    </p>
    <div class="ml-auto flex items-center gap-2" role="toolbar" aria-label="Source code">
      <button type="button" class="btn btn-sm" disabled={working || !stack.approved} title={stack.approved ? undefined : "Approve the stack first."} onclick={() => (confirming = true)}>Fork…</button>
      <RowMenu name="Source code" items={[{ label: "Copy to a new draft…", onSelect: () => (copying = true) }]} />
    </div>
  </div>

  {#if dirty}
    <div role="status" class="flex flex-wrap items-center gap-2 border-b border-line bg-warn-soft px-3 py-1.5 text-warn">
      <p class="min-w-48 flex-1">
        {#if src.local}
          Not saved. Edit the files in their folder and commit, or fork to edit them here.
        {:else}
          Not saved: {src.repo} is read only here. Fork to keep your changes.
        {/if}
      </p>
      <button type="button" class="btn btn-sm" disabled={working} onclick={() => (buffer = bufferOf(files.files))}>Discard</button>
      <button
        type="button"
        class="btn btn-sm btn-primary"
        disabled={working || !stack.approved}
        title={stack.approved ? undefined : "Approve the stack first."}
        onclick={() => fork(true)}
      >
        {working ? "Forking…" : "Fork and save"}
      </button>
    </div>
  {/if}
  {#if error}
    <p role="alert" class="border-b border-line bg-bad-soft px-3 py-1.5 text-bad">{error}</p>
  {/if}

  <div class="flex min-h-0 flex-1 flex-col md:h-[65vh] md:min-h-80 md:flex-none md:flex-row xl:h-auto xl:flex-1">
    <div class="flex max-h-48 min-h-0 shrink-0 flex-col border-b border-line md:max-h-none md:w-56 md:border-r md:border-b-0">
      <FileList {buffer} {open} {changed} readonly onOpen={(p) => (open = p)} />
    </div>
    <div class="flex min-h-0 min-w-0 flex-1 flex-col">
      <div class="flex shrink-0 border-b border-line bg-sunken">
        <span class="-mb-px min-w-0 truncate border-t-2 border-r border-t-accent border-r-line bg-surface px-3 py-1.5">
          {open}{#if changed.includes(open)}<span class="ml-1 text-accent" aria-label="(changed)">•</span>{/if}
        </span>
      </div>
      <div class="h-[60vh] min-h-72 md:h-auto md:min-h-0 md:flex-1">
        {#if buffer[open] === null}
          <p class="p-4 text-muted">A binary file, or larger than 256 KB: not shown here.</p>
        {:else}
          {#key open}
            <CodeEditor bare value={buffer[open] ?? ""} file={open} label={open} onChange={(v) => (buffer[open] = v)} />
          {/key}
        {/if}
      </div>
    </div>
  </div>
</div>

<Modal
  open={confirming}
  onOpenChange={(o) => (confirming = o)}
  title="Fork {title}?"
  description="It runs from its own copy on this host, editable here, and stops following its repo."
>
  <p class="text-muted">Starts from {shortRev(stack.approved?.rev ?? files.rev)}, without the repo's history.</p>
  {#if error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
  {#snippet actions()}
    <button type="button" class="btn btn-primary" disabled={working || !stack.approved} title={stack.approved ? undefined : "Approve the stack first."} onclick={() => fork(dirty)}>
      {working ? "Forking…" : "Fork"}
    </button>
  {/snippet}
</Modal>

<Modal
  open={pendingNav !== null}
  onOpenChange={(o) => {
    if (!o) pendingNav = null;
  }}
  title="Leave without saving?"
>
  <p class="text-muted">Unsaved changes will be lost.</p>
  {#snippet actions()}
    <button type="button" class="btn btn-danger" onclick={confirmLeave}>Leave</button>
  {/snippet}
</Modal>

{#if copying}<ForkStack {stack} onClose={() => (copying = false)} />{/if}
