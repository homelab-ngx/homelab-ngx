<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { untrack } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import { beforeNavigate, goto } from "$app/navigation";
  import { api, call, describe, ProblemError, type Schema } from "#lib/api/client.ts";
  import CodeEditor from "#lib/components/CodeEditor.svelte";
  import FileList from "#lib/components/drafts/FileList.svelte";
  import DeleteEntryDialog from "#lib/components/storage/DeleteEntryDialog.svelte";
  import { ago, bytes } from "#lib/format.ts";
  import { ancestors, browseHref, editHref, hostPathProblem, hostTree, joinPath, parentOf, relativeTo } from "#lib/storage.ts";

  type HostFile = Schema<"HostFile">;
  type Entry = Schema<"Entry">;

  interface Props {
    /** The folder the file list starts at. */
    root: string;
    /** The open file, below root. */
    path: string;
  }
  let { root, path }: Props = $props();

  const client = useQueryClient();
  const open = $derived(relativeTo(root, path) ?? path);
  const fileKey = (p: string) => ["daemon", "storage", "file", p];

  const file = createQuery(() => ({
    queryKey: fileKey(path),
    queryFn: () => call<HostFile>(api.GET("/storage/file", { params: { query: { path } } })),
    retry: false,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  }));

  let error = $state("");
  /** The open file changed on disk since it was read: Save asks how to go on. */
  let conflict = $state(false);
  let saving = $state(false);
  /** The path typed for Save as, while its field is open. */
  let saveAs = $state<string | null>(null);
  /** The row whose delete dialog is open. */
  let deleting = $state<{ path: string; type: Entry["type"] } | null>(null);

  /** What was typed into the open file, by path; a file without an entry is as saved. */
  let edits = $state<Record<string, string>>({});
  const dirty = $derived(path in edits);
  const text = $derived(edits[path] ?? file.data?.content ?? "");

  // The file list: each folder is read when it is first opened.
  let listings = $state<Record<string, Entry[]>>({});
  const expanded = new SvelteSet<string>(untrack(() => ancestors(open)));
  const rows = $derived(hostTree(listings, expanded));

  async function list(dir: string) {
    try {
      const at = dir ? joinPath(root, dir) : root;
      const l = await client.fetchQuery({
        queryKey: ["daemon", "storage", "browse", at],
        queryFn: () => call<Schema<"Listing">>(api.GET("/storage/browse", { params: { query: { path: at } } })),
        staleTime: 0,
      });
      listings[dir] = l.entries;
    } catch (e) {
      error = describe(e);
    }
  }
  untrack(() => ["", ...expanded].forEach((d) => void list(d)));

  function toggle(dir: string) {
    if (expanded.delete(dir)) return;
    expanded.add(dir);
    if (!(dir in listings)) void list(dir);
  }

  function show(p: string) {
    conflict = false;
    return goto(editHref(p, root), { replace: true, reset: false });
  }

  /** Writes content to target and refreshes its folder's listing. */
  async function write(target: string, content: string, opts: { version?: string; create?: boolean }) {
    const saved = await call<HostFile>(api.PUT("/storage/file", { body: { path: target, content, ...opts } }));
    client.setQueryData(fileKey(saved.path), { ...saved, content });
    const dir = parentOf(saved.path);
    const rel = dir === root ? "" : relativeTo(root, dir);
    if (rel !== null) await list(rel);
    return saved;
  }

  async function save(force = false) {
    if (!dirty || saving || !file.data) return;
    saving = true;
    error = "";
    try {
      const sent = text;
      await write(path, sent, { version: force ? undefined : file.data.version });
      // What was typed during the save stays unsaved.
      if (edits[path] === sent) delete edits[path];
      conflict = false;
    } catch (e) {
      if (e instanceof ProblemError && e.code === "changed_on_disk") conflict = true;
      else error = describe(e);
    } finally {
      saving = false;
    }
  }

  function reload() {
    delete edits[path];
    conflict = false;
    void client.invalidateQueries({ queryKey: fileKey(path) });
  }

  /** The host path a typed name stands for: absolute, or inside root. */
  const target = (typed: string) => (typed.startsWith("/") ? typed : joinPath(root, typed));

  async function submitSaveAs(e: SubmitEvent) {
    e.preventDefault();
    if (saveAs === null || !saveAs.trim() || saving) return;
    saving = true;
    error = "";
    try {
      const saved = await write(target(saveAs.trim()), text, { create: true });
      delete edits[path];
      saveAs = null;
      await show(saved.path);
    } catch (e) {
      error = describe(e);
    } finally {
      saving = false;
    }
  }

  async function newFile(rel: string) {
    error = "";
    try {
      const saved = await write(joinPath(root, rel), "", { create: true });
      for (const a of ancestors(rel)) if (!expanded.has(a)) toggle(a);
      await show(saved.path);
    } catch (e) {
      error = describe(e);
    }
  }

  function deleted() {
    const gone = deleting!.path;
    const rel = relativeTo(root, gone);
    for (const p of Object.keys(listings)) if (rel && (p === rel || p.startsWith(`${rel}/`))) delete listings[p];
    void list(relativeTo(root, parentOf(gone)) ?? "");
    if (gone === path || path.startsWith(`${gone}/`)) {
      delete edits[path];
      void goto(browseHref(root));
    }
  }

  beforeNavigate((nav) => {
    if (dirty && !confirm("Unsaved changes. Leave anyway?")) nav.cancel();
    else delete edits[path];
  });

  function keydown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key === "s") {
      e.preventDefault();
      void save();
    }
  }
</script>

<svelte:window
  onkeydown={keydown}
  onbeforeunload={(e) => {
    if (dirty) e.preventDefault();
  }}
/>

<!-- The Workbench's layout without its Output panel and assistant: the file
     list beside one tab, and a status bar. -->
<div class="flex flex-col overflow-hidden rounded-md border border-line bg-surface xl:h-[calc(100dvh-4.25rem)] xl:min-h-[36rem]">
  <div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-line bg-sunken px-3 py-1.5">
    <div class="flex min-w-0 items-baseline gap-1.5">
      <a class="link text-muted" href={browseHref(root)}>Files</a>
      <span class="text-muted" aria-hidden="true">/</span>
      <h1 class="truncate font-semibold">{root}</h1>
    </div>
    <div class="ml-auto flex flex-wrap items-center gap-2" role="toolbar" aria-label="File">
      {#if saveAs !== null}
        <form class="flex items-center gap-2" onsubmit={submitSaveAs}>
          <input
            class="input w-64 px-2 py-0.5 text-xs"
            aria-label="Save as"
            bind:value={saveAs}
            {@attach (el) => el.select()}
            onkeydown={(e) => {
              if (e.key === "Escape") saveAs = null;
            }}
          />
          <button type="submit" class="btn btn-sm btn-primary" disabled={!saveAs.trim() || saving}>Save</button>
          <button type="button" class="btn btn-sm" onclick={() => (saveAs = null)}>Cancel</button>
        </form>
      {:else}
        <button type="button" class="btn btn-sm" disabled={!dirty || saving} onclick={() => save()} title="Ctrl+S">
          {saving ? "Saving…" : dirty ? "Save" : "Saved"}
        </button>
        <button type="button" class="btn btn-sm" disabled={!file.data || file.data.binary} onclick={() => (saveAs = open)}>Save as…</button>
      {/if}
    </div>
  </div>

  {#if error}
    <p role="alert" class="border-b border-line bg-bad-soft px-3 py-1.5 text-bad">{error}</p>
  {/if}
  {#if conflict}
    <div role="alert" class="flex flex-wrap items-center gap-2 border-b border-line bg-warn-soft px-3 py-1.5">
      <p>{open} changed on disk since it was opened.</p>
      <button type="button" class="btn btn-sm ml-auto" onclick={reload}>Reload</button>
      <button type="button" class="btn btn-sm btn-danger" onclick={() => save(true)}>Save anyway</button>
    </div>
  {/if}

  <div class="flex min-h-0 flex-1 flex-col md:h-[70vh] md:min-h-80 md:flex-none md:flex-row xl:h-auto xl:flex-1">
    <div class="flex max-h-48 min-h-0 shrink-0 flex-col border-b border-line md:max-h-none md:w-56 md:border-r md:border-b-0">
      <FileList
        {rows}
        {expanded}
        {open}
        changed={dirty ? [open] : []}
        required={[]}
        onToggle={toggle}
        problemOf={(p) => hostPathProblem(p, rows)}
        onOpen={(p) => show(joinPath(root, p))}
        onNew={newFile}
        onDelete={(p) => (deleting = { path: joinPath(root, p), type: rows.find((r) => r.path === p)?.kind === "folder" ? "dir" : "file" })}
      />
    </div>
    <div class="flex min-h-0 min-w-0 flex-1 flex-col">
      <div class="flex shrink-0 border-b border-line bg-sunken">
        <span class="-mb-px truncate border-t-2 border-r border-t-accent border-r-line bg-surface px-3 py-1.5">
          {open}{#if dirty}<span class="ml-1 text-accent" aria-label="(changed)">•</span>{/if}
        </span>
      </div>
      <div class="h-[60vh] min-h-72 md:h-auto md:min-h-0 md:flex-1">
        {#if file.error}
          <p role="alert" class="p-4 text-bad">{describe(file.error)}</p>
        {:else if !file.data}
          <p class="p-4 text-muted">Reading the file…</p>
        {:else if file.data.binary}
          <p class="p-4 text-muted">A binary file, or larger than 1 MB: not edited here.</p>
        {:else}
          {#key path}
            <!-- Typing the saved text back, or a reload, leaves the file unchanged. -->
            <CodeEditor
              bare
              value={text}
              file={open}
              label={open}
              onChange={(v) => {
                if (v === file.data?.content) delete edits[path];
                else edits[path] = v;
              }}
            />
          {/key}
        {/if}
      </div>
    </div>
  </div>

  <div class="flex flex-wrap items-center gap-x-3 border-t border-line bg-sunken px-3 py-1 text-xs text-muted tabular-nums">
    {#if file.data}
      <span>{dirty ? "Unsaved" : "Saved"} · {bytes(file.data.size)} · modified {ago(file.data.modified_at)}</span>
      <span class="[overflow-wrap:anywhere]">In {file.data.root}</span>
    {/if}
  </div>
</div>

{#if deleting}
  <DeleteEntryDialog path={deleting.path} type={deleting.type} onClose={() => (deleting = null)} onDeleted={deleted} />
{/if}
