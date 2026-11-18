<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { Sparkles } from "@lucide/svelte";
  import { onDestroy, untrack } from "svelte";
  import { beforeNavigate, goto } from "$app/navigation";
  import { api, call, describe, ProblemError, type Draft, type DraftCheck, type DraftFiles, type Stack } from "#lib/api/client.ts";
  import { fetchAssistant, keys } from "#lib/api/queries.ts";
  import CodeEditor from "#lib/components/CodeEditor.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import { changedFiles, fileOf } from "#lib/drafts.ts";
  import { manifestAssist, type SchemaNode } from "#lib/manifest-assist.ts";
  import { NixLanguageServer } from "#lib/lsp.svelte.ts";
  import { FILES, type FileKey } from "#lib/editor.ts";
  import { ago, shortRev } from "#lib/format.ts";
  import AssistantPanel from "./AssistantPanel.svelte";
  import DraftOutput from "./DraftOutput.svelte";

  interface Props {
    /** The draft as saved; it changes on a save and while a build runs. */
    draft: Draft;
  }
  let { draft }: Props = $props();

  const client = useQueryClient();
  const assistant = createQuery(() => ({ queryKey: keys.assistant, queryFn: fetchAssistant }));
  const assistantOn = $derived(!!assistant.data && assistant.data.provider !== "none");
  const schema = createQuery(() => ({
    queryKey: keys.manifestSchema,
    queryFn: async () => (await call(api.GET("/stacks-manifest-schema"))) as SchemaNode,
    staleTime: Infinity,
  }));
  const path = $derived({ params: { path: { name: draft.name } } });

  /** The unsaved copy of the files the editor works on. */
  let buffer = $state<DraftFiles>({ ...untrack(() => draft.files) });
  let tab = $state<FileKey>("manifest");
  let message = $state("");
  let error = $state("");
  let working = $state<"" | "save" | "check" | "build" | "install" | "delete">("");
  let confirmDelete = $state(false);

  // The assistant's side bar stays as it was left, per browser.
  const SIDEBAR_KEY = "draft-assistant-open";
  let assistantOpen = $state(readOpen());
  function readOpen(): boolean {
    try {
      return localStorage.getItem(SIDEBAR_KEY) !== "0";
    } catch {
      return true;
    }
  }
  function toggleAssistant() {
    assistantOpen = !assistantOpen;
    try {
      localStorage.setItem(SIDEBAR_KEY, assistantOpen ? "1" : "0");
    } catch {
      // A private window keeps it for this page only.
    }
  }

  /** The last check, with the files it read (as JSON) to tell when it is stale. */
  let checked = $state<{ result: DraftCheck; files: string } | null>(
    untrack(() => (draft.check?.saved && draft.check.rev === draft.rev ? { result: draft.check, files: JSON.stringify(draft.files) } : null)),
  );

  const changed = $derived(changedFiles(draft.files, buffer));
  const dirty = $derived(changed.length > 0);
  const checkStale = $derived(!!checked && checked.files !== JSON.stringify(buffer));
  /** The build the operator cleared from the output; a newer one shows again. */
  let clearedBuild = $state<string | null>(null);
  const shownBuild = $derived(draft.build && draft.build.started_at !== clearedBuild ? draft.build : undefined);
  const installedAs = $derived(draft.installed_as[0]);
  const buildRunning = $derived(draft.build?.result === "running");
  /** What Fix last error fixes: the check's problems and the failed build's error; "" for nothing. */
  const fixText = $derived(
    [
      ...(checked && !checked.result.ok ? checked.result.problems.map((p) => `${p.file}: ${p.message}`) : []),
      ...(draft.build?.result === "failed" ? [draft.build.error || "The last build failed."] : []),
    ].join("\n"),
  );

  function setDraft(d: Draft) {
    client.setQueryData(keys.draft(d.name), d);
    void client.invalidateQueries({ queryKey: keys.drafts, exact: true });
  }

  async function act<T>(kind: typeof working, f: () => Promise<T>): Promise<T | undefined> {
    error = "";
    working = kind;
    try {
      return await f();
    } catch (err) {
      error = describe(err);
    } finally {
      working = "";
    }
  }

  async function save() {
    if (!dirty || working) return;
    const d = await act("save", () =>
      call<Draft>(api.PUT("/drafts/{name}", { ...path, body: { files: buffer, message: message.trim() || undefined } })),
    );
    if (!d) return;
    message = "";
    setDraft(d);
    // A stack installed from the draft now has the commit as an update.
    if (d.installed_as.length) void client.invalidateQueries({ queryKey: keys.stacks });
  }

  async function check() {
    const files = JSON.stringify(buffer);
    const c = await act("check", () =>
      call<DraftCheck>(api.POST("/drafts/{name}/check", { ...path, body: dirty ? { files: buffer } : {} })),
    );
    if (c) checked = { result: c, files };
  }

  async function build() {
    const d = await act("build", () => call<Draft>(api.POST("/drafts/{name}/build", path)));
    if (d) setDraft(d);
  }

  /** Checks the saved files, adds the stack from the draft's repo and goes on to its grants. */
  async function install() {
    await act("install", async () => {
      const c = await call<DraftCheck>(api.POST("/drafts/{name}/check", { ...path, body: {} }));
      checked = { result: c, files: JSON.stringify(draft.files) };
      if (!c.ok) {
        error = "Fix what the check found, save, then install.";
        return;
      }
      let s: Stack;
      try {
        s = await call<Stack>(api.POST("/stacks", { body: { source: draft.source, name: draft.name } }));
      } catch (err) {
        if (err instanceof ProblemError && err.code === "stack_exists") {
          throw new Error(`A stack named ${draft.name} is already installed.`);
        }
        throw err;
      }
      await client.invalidateQueries({ queryKey: keys.all });
      await goto(`/stacks/add?stack=${encodeURIComponent(s.name)}`);
    });
  }

  async function remove() {
    const ok = await act("delete", async () => {
      await call(api.DELETE("/drafts/{name}", path));
      return true;
    });
    if (!ok) return;
    buffer = { ...draft.files }; // nothing left to lose
    client.removeQueries({ queryKey: keys.draft(draft.name) });
    await client.invalidateQueries({ queryKey: keys.drafts });
    await goto("/stacks");
  }

  /** nixd for flake.nix, started the first time the file is shown. */
  let nixLsp = $state<NixLanguageServer | null>(null);
  onDestroy(() => nixLsp?.close());
  const nixReady = $derived(nixLsp?.status === "ready");

  function show(key: FileKey) {
    tab = key;
    if (key === "flake" && !nixLsp) nixLsp = new NixLanguageServer(draft.name);
  }

  /** Language help per file: the manifest's schema and a Check of the text being typed; nixd for flake.nix. */
  const extensionsFor = (key: FileKey) =>
    key === "flake"
      ? nixLsp && nixReady
        ? [nixLsp.extension()]
        : []
      : key === "manifest"
      ? [
          manifestAssist(
            () => schema.data,
            async (manifest) => {
              const files = { ...$state.snapshot(buffer), manifest } as DraftFiles;
              const c = await call<DraftCheck>(api.POST("/drafts/{name}/check", { ...path, body: { files } }));
              checked = { result: c, files: JSON.stringify(files) };
              return c.problems.filter((p) => p.file === "homelab-stack.json").map((p) => p.message);
            },
          ),
        ]
      : [];

  beforeNavigate((nav) => {
    if (dirty && working !== "delete" && !confirm("Unsaved changes. Leave anyway?")) nav.cancel();
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

<!-- A light VS Code workbench: title bar and toolbar, file tabs over the
     editor, one output panel under it, the assistant in a side bar opened from
     the activity bar on the right, and a status bar. -->
<div class="flex flex-col overflow-hidden rounded-md border border-line bg-surface xl:h-[calc(100dvh-4.25rem)] xl:min-h-[36rem]">
  <div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-line bg-sunken px-3 py-1.5">
    <div class="flex min-w-0 items-baseline gap-1.5">
      <a class="link text-muted" href="/stacks">Stacks</a>
      <span class="text-muted" aria-hidden="true">/</span>
      <h1 class="truncate font-semibold">{draft.name}</h1>
    </div>
    <div class="ml-auto flex flex-wrap items-center gap-2" role="toolbar" aria-label="Draft">
      {#if dirty}
        <input class="input w-56 px-2 py-0.5 text-xs" bind:value={message} aria-label="What changed" placeholder="What changed (optional)" maxlength="500" />
      {/if}
      <button type="button" class="btn btn-sm" disabled={!dirty || !!working} onclick={save} title="Ctrl+S">
        {working === "save" ? "Saving…" : dirty ? "Save" : "Saved"}
      </button>
      <button type="button" class="btn btn-sm" disabled={!!working} onclick={check}>{working === "check" ? "Checking…" : "Check"}</button>
      <button type="button" class="btn btn-sm" disabled={dirty || !!working || buildRunning} onclick={build} title={dirty ? "Save first." : undefined}>
        {buildRunning ? "Building…" : "Build"}
      </button>
      {#if installedAs}
        <a class="btn btn-sm btn-primary" href="/stacks/{installedAs}">Open stack</a>
      {:else}
        <button type="button" class="btn btn-sm btn-primary" disabled={dirty || !!working} onclick={install} title={dirty ? "Save first." : undefined}>
          {working === "install" ? "Reading…" : "Install…"}
        </button>
      {/if}
      <RowMenu name={draft.name} items={installedAs ? [] : [{ label: "Delete draft…", danger: true, onSelect: () => (confirmDelete = true) }]} />
    </div>
  </div>

  {#if error}
    <p role="alert" class="border-b border-line bg-bad-soft px-3 py-1.5 text-bad">{error}</p>
  {/if}
  {#if confirmDelete}
    <div class="flex flex-wrap items-center gap-2 border-b border-line bg-bad-soft px-3 py-1.5">
      <p>Delete {draft.name} and every saved version of it?</p>
      <button type="button" class="btn btn-sm btn-danger ml-auto" disabled={!!working} onclick={remove}>Delete</button>
      <button type="button" class="btn btn-sm" onclick={() => (confirmDelete = false)}>Cancel</button>
    </div>
  {/if}

  <div class="flex min-h-0 flex-1 flex-col xl:flex-row">
    <section aria-label="Files" class="flex min-h-0 min-w-0 flex-1 flex-col">
      <div role="tablist" aria-label="Files" class="flex shrink-0 border-b border-line bg-sunken">
        {#each FILES as f (f.key)}
          <button
            type="button"
            role="tab"
            aria-selected={tab === f.key}
            class="-mb-px border-t-2 border-r border-t-transparent border-r-line px-3 py-1.5 text-muted hover:text-fg aria-selected:border-t-accent aria-selected:bg-surface aria-selected:text-fg"
            onclick={() => show(f.key)}
          >
            {f.path}{#if changed.includes(f.key)}<span class="ml-1 text-accent" aria-label="(changed)">•</span>{/if}
          </button>
        {/each}
      </div>
      <div class="h-[60vh] min-h-72 xl:h-auto xl:min-h-0 xl:flex-1" role="tabpanel">
        <!-- Made again once nixd is ready, so the editor takes its extension. -->
        {#key `${tab}:${tab === "flake" && nixReady}`}
          <CodeEditor bare extensions={extensionsFor(tab)} value={buffer[tab]} file={tab} label={FILES.find((f) => f.key === tab)!.path} onChange={(v) => (buffer[tab] = v)} />
        {/key}
      </div>
      <div class="h-64 shrink-0 border-t border-line xl:h-[30%]">
        <DraftOutput
          check={checked?.result}
          {checkStale}
          build={shownBuild}
          onClear={() => {
            checked = null;
            clearedBuild = draft.build?.started_at ?? null;
          }}
          buildStale={!!draft.build && (draft.build.rev !== draft.rev || dirty)}
          onShow={(path) => show(fileOf(path))}
        />
      </div>
    </section>

    {#if assistantOn && assistantOpen}
      <aside class="min-h-0 border-t border-line xl:w-[30rem] xl:shrink-0 xl:overflow-auto xl:border-t-0 xl:border-l">
        <AssistantPanel name={draft.name} {buffer} {fixText} model={assistant.data!.model} onApply={(f) => (buffer = f)} />
      </aside>
    {/if}
    {#if assistantOn}
      <div role="toolbar" aria-label="Side bar" class="flex shrink-0 gap-1 border-line bg-sunken p-1 max-xl:order-first max-xl:border-b xl:flex-col xl:border-l">
        <button
          type="button"
          class="rounded-md p-1.5 text-muted hover:text-fg aria-pressed:bg-accent-soft aria-pressed:text-accent"
          aria-pressed={assistantOpen}
          aria-label="Assistant"
          title="Assistant"
          onclick={toggleAssistant}
        >
          <Sparkles class="size-5" aria-hidden="true" />
        </button>
      </div>
    {/if}
  </div>

  <div class="flex flex-wrap items-center gap-x-3 border-t border-line bg-sunken px-3 py-1 text-xs text-muted tabular-nums">
    <span>{dirty ? `${changed.length} unsaved` : "Saved"} · {ago(draft.saved_at)} · {shortRev(draft.rev)}</span>
    {#if installedAs}
      <span>Installed as <a class="link" href="/stacks/{installedAs}">{installedAs}</a></span>
    {/if}
    {#if nixLsp}
      <span title={nixLsp.status === "off" ? nixLsp.reason : undefined}>
        Nix: {nixLsp.status === "ready" ? "nixd" : nixLsp.status === "starting" ? "starting…" : "off"}
      </span>
    {/if}
    {#if assistant.data && !assistantOn}
      <a class="link ml-auto" href="/settings/assistant">Set up the AI assistant</a>
    {/if}
  </div>
</div>
