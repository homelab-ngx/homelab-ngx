<script lang="ts">
  import { ChevronDown, ChevronRight, FilePlus } from "@lucide/svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import { fileTree, pathProblem, REQUIRED, type Buffer, type TreeRow } from "#lib/drafts.ts";

  interface Props {
    /** The editor's files. */
    buffer?: Buffer;
    /**
     * The rows to list instead of the buffer's: a tree read one folder at a
     * time. Its folders open and close through onToggle.
     */
    rows?: TreeRow[];
    /** The open folders of rows. */
    expanded?: ReadonlySet<string>;
    onToggle?: (folder: string) => void;
    /** Why a new file cannot be at path; the buffer's checks by default. */
    problemOf?: (path: string) => string;
    /** Files without a Row Menu. */
    required?: string[];
    /** The file the editor shows. */
    open: string;
    /** The paths with unsaved changes. */
    changed: string[];
    onOpen: (path: string) => void;
    /** The files can be read, not made, renamed or deleted. */
    readonly?: boolean;
    /** Make an empty file at path. */
    onNew?: (path: string) => void;
    onRename?: (from: string, to: string) => void;
    onDelete?: (path: string) => void;
  }
  let {
    buffer = {},
    rows: given,
    expanded,
    onToggle,
    problemOf,
    required = REQUIRED,
    open,
    changed,
    readonly = false,
    onOpen,
    onNew,
    onRename,
    onDelete,
  }: Props = $props();

  const rows = $derived(given ?? fileTree(Object.keys(buffer)));

  /** The name being typed: a new file, or a file's new name. */
  let naming = $state<{ from: string | null; path: string } | null>(null);
  const others = $derived.by(() => {
    if (!naming?.from) return buffer;
    const { [naming.from]: _, ...rest } = buffer;
    return rest;
  });
  const problem = $derived.by(() => {
    if (!naming || naming.path === naming.from) return "";
    const path = naming.path.trim();
    return problemOf ? problemOf(path) : pathProblem(path, others);
  });

  function startNew() {
    const dir = open.includes("/") ? open.slice(0, open.lastIndexOf("/") + 1) : "";
    naming = { from: null, path: dir };
  }

  /** Makes or renames the file; an unchanged name just closes the field. */
  function commit() {
    if (!naming || problem) return;
    const { from } = naming;
    const path = naming.path.trim();
    naming = null;
    if (from === null) onNew?.(path);
    else if (path !== from) onRename?.(from, path);
  }

  function submit(e: SubmitEvent) {
    e.preventDefault();
    commit();
  }

  const focus = (el: HTMLInputElement) => {
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
  };
</script>

{#snippet nameField(label: string)}
  <form class="flex flex-col gap-1 px-2 py-1" onsubmit={submit}>
    <input
      class="input px-2 py-0.5 font-mono text-xs"
      aria-label={label}
      bind:value={naming!.path}
      maxlength="256"
      {@attach focus}
      onkeydown={(e) => {
        if (e.key === "Escape") naming = null;
      }}
      onblur={() => {
        // Leaving the field keeps a name that can be taken, like Enter.
        if (problem) naming = null;
        else commit();
      }}
    />
    {#if problem}<p class="font-sans text-xs text-bad" role="alert">{problem}</p>{/if}
  </form>
{/snippet}

<nav aria-label="Files" class="flex min-h-0 flex-1 flex-col">
  <div class="flex items-center gap-2 border-b border-line bg-sunken px-3 py-1">
    <h2 class="label">Files</h2>
    {#if !readonly}
      <button type="button" class="ml-auto rounded-md p-1 text-muted hover:text-fg" aria-label="New file" title="New file" onclick={startNew}>
        <FilePlus class="size-4" aria-hidden="true" />
      </button>
    {/if}
  </div>
  <ul class="min-h-0 flex-1 overflow-auto py-1 font-mono text-xs">
    {#if naming && naming.from === null}
      <li>{@render nameField("New file's path")}</li>
    {/if}
    {#each rows as row (row.kind + row.path)}
      <li>
        {#if row.kind === "folder" && onToggle}
          {@const on = expanded?.has(row.path) ?? false}
          <div class="group flex items-center text-muted hover:bg-sunken hover:text-fg">
            <button
              type="button"
              class="flex min-w-0 flex-1 items-center gap-1 py-0.5 pr-1 text-left"
              style="padding-left: {0.75 + row.depth * 0.75}rem"
              aria-expanded={on}
              onclick={() => onToggle(row.path)}
            >
              {#if on}<ChevronDown class="size-3 shrink-0" aria-hidden="true" />{:else}<ChevronRight class="size-3 shrink-0" aria-hidden="true" />{/if}
              <span class="truncate">{row.name}/</span>
            </button>
            {#if !readonly && onDelete}
              <span class="shrink-0 pr-1 opacity-0 group-focus-within:opacity-100 group-hover:opacity-100">
                <RowMenu name={row.path} items={[{ label: "Delete…", danger: true, onSelect: () => onDelete(row.path) }]} />
              </span>
            {/if}
          </div>
        {:else if row.kind === "folder"}
          <div class="truncate px-3 py-0.5 text-muted" style="padding-left: {0.75 + row.depth * 0.75}rem">{row.name}/</div>
        {:else if naming?.from === row.path}
          {@render nameField(`New path of ${row.path}`)}
        {:else}
          <div class="group flex items-center {open === row.path ? 'bg-accent-soft text-fg' : 'text-muted hover:bg-sunken hover:text-fg'}">
            <button
              type="button"
              class="min-w-0 flex-1 truncate py-0.5 pr-1 text-left"
              style="padding-left: {0.75 + row.depth * 0.75 + (onToggle ? 1 : 0)}rem"
              aria-current={open === row.path ? "page" : undefined}
              onclick={() => onOpen(row.path)}
            >
              {row.name}{#if changed.includes(row.path)}<span class="ml-1 text-accent" aria-label="(changed)">•</span>{/if}
            </button>
            {#if !readonly && !required.includes(row.path)}
              <span class="shrink-0 pr-1 opacity-0 group-focus-within:opacity-100 group-hover:opacity-100">
                <RowMenu
                  name={row.path}
                  items={[
                    ...(onRename && buffer[row.path] !== null ? [{ label: "Rename…", onSelect: () => (naming = { from: row.path, path: row.path }) }] : []),
                    // Without a buffer the file goes from disk, so a dialog asks first.
                    { label: given ? "Delete…" : "Delete", danger: true, onSelect: () => onDelete?.(row.path) },
                  ]}
                />
              </span>
            {/if}
          </div>
        {/if}
      </li>
    {/each}
  </ul>
</nav>
