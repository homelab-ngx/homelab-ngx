<script lang="ts">
  import { ArrowDown, ArrowUp, File, Folder, Link2 } from "@lucide/svelte";
  import { createQuery } from "@tanstack/svelte-query";
  import { page } from "$app/state";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import DeleteEntryDialog from "#lib/components/storage/DeleteEntryDialog.svelte";
  import { ago, bytes, stamp } from "#lib/format.ts";
  import { type BrowseKey, type BrowseRow, browseDefault, browseHref, browseRows, browseSort, crumbs, editHref } from "#lib/storage.ts";

  type Listing = Schema<"Listing">;
  type FolderFigures = Schema<"FolderFigures">;

  const path = $derived(page.url.searchParams.get("path") || "/");

  const listing = createQuery(() => ({
    queryKey: ["daemon", "storage", "browse", path],
    queryFn: () => call<Listing>(api.GET("/storage/browse", { params: { query: { path } } })),
    retry: false,
  }));
  // The figures come from one slow run per subvolume or stack folder, so they
  // are a query of their own: the list never waits for them.
  const figures = createQuery(() => ({
    queryKey: ["daemon", "storage", "browse-usage", path],
    queryFn: () => call<FolderFigures>(api.GET("/storage/browse/usage", { params: { query: { path } } })),
    enabled: !!listing.data?.root,
    staleTime: 60_000,
    retry: false,
  }));

  const here = $derived(listing.data?.path ?? path);
  const trail = $derived(crumbs(here));
  /** Above the subvolumes and stack folders there is only the way down. */
  const above = $derived(listing.data ? !listing.data.root : false);
  let sort = $state(browseDefault);
  const rows = $derived(browseRows(listing.data?.entries ?? [], figures.data, sort));
  /** The pool columns show while the figures load and once there are some. */
  const measured = $derived(!above && (figures.isPending || !!figures.data?.available));
  /** Where the folder sizes stand; its help says what the size columns count. */
  const sizesLabel = $derived(
    figures.isPending
      ? "Measuring folders…"
      : figures.data?.available
        ? `Sizes measured ${ago(figures.data.measured_at)}`
        : figures.data?.reason === "failed" || figures.error
          ? "Folder sizes could not be measured"
          : "No folder sizes on this disk",
  );
  const child = (name: string) => `${here === "/" ? "" : here}/${name}`;
  /** The row whose delete dialog is open. */
  let deleting = $state<BrowseRow | null>(null);
</script>

<svelte:head><title>{here} · Storage · Homelab-ngx</title></svelte:head>

<a class="link text-xs" href="/storage">← Storage</a>
<h1 class="mt-2 text-xl font-semibold">
  <Help label="Files">
    The subvolumes and the stacks' folders. A file opens in the editor. Above those folders only the way down to them is listed.
  </Help>
</h1>

<nav aria-label="Folder" class="mt-2 flex flex-wrap items-center gap-x-1 gap-y-0.5">
  {#each trail as c, i (c.path)}
    {#if i > 1}<span class="text-muted" aria-hidden="true">/</span>{/if}
    {#if i === trail.length - 1}
      <span aria-current="page" class="[overflow-wrap:anywhere]">{c.label}</span>
    {:else}
      <a class="link [overflow-wrap:anywhere]" href={browseHref(c.path)}>{c.label}</a>
    {/if}
  {/each}
</nav>

{#if listing.error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(listing.error)}</p>
{:else if listing.data}
  <div class="mt-4 mb-2 flex flex-wrap items-center justify-between gap-2 text-xs text-muted">
    <span>
      {#if listing.data.truncated}
        First {listing.data.entries.length.toLocaleString()} of {listing.data.total.toLocaleString()} items by name
      {:else}
        {listing.data.total.toLocaleString()} item{listing.data.total === 1 ? "" : "s"}
      {/if}
    </span>
    {#if !above}
      <Help label={sizesLabel}>
          A folder's size counts data its files share once, before compression and without snapshots. Freed if deleted is the space
          deleting it returns. Shared is data that files elsewhere also use, so it stays.
      </Help>
    {/if}
  </div>

  <div class="relative overflow-x-auto rounded-lg border border-line bg-surface">
    <table class="w-full border-collapse text-left tabular-nums">
      <thead>
        <tr class="border-b border-line bg-sunken">
          {@render th("name", "Name")}
          {#if !above}
            {@render th("size", "Size", "text-right")}
            {#if measured}
              {@render th("exclusive", "Freed if deleted", "text-right max-sm:hidden")}
              {@render th("shared", "Shared", "text-right max-md:hidden")}
            {/if}
            {@render th("modified", "Modified", "max-md:hidden")}
            {@render th("owner", "Owner", "max-lg:hidden")}
            <th scope="col" class="w-0 px-3 py-2"><span class="sr-only">Actions</span></th>
          {/if}
        </tr>
      </thead>
      <tbody>
        {#each rows as r (r.name)}
          {@const waiting = r.type === "dir" && figures.isPending}
          <tr class="border-b border-line last:border-0">
            <td class="px-3 py-2">
              <span class="flex items-start gap-2">
                {#if r.type === "dir"}
                  <Folder class="mt-0.5 size-4 shrink-0 text-muted" aria-hidden="true" />
                  <a class="link [overflow-wrap:anywhere]" href={browseHref(child(r.name))}>{r.name}</a>
                {:else}
                  {#if r.type === "symlink"}
                    <Link2 class="mt-0.5 size-4 shrink-0 text-muted" aria-hidden="true" />
                  {:else}
                    <File class="mt-0.5 size-4 shrink-0 text-muted" aria-hidden="true" />
                  {/if}
                  <span class="[overflow-wrap:anywhere]">
                    {#if r.type === "file" && !above}
                      <a class="link" href={editHref(child(r.name))}>{r.name}</a>
                    {:else}
                      {r.name}
                    {/if}
                    {#if r.type === "symlink"}<span class="text-xs text-muted">link</span>{/if}
                  </span>
                {/if}
              </span>
              {#if !above}
                <span class="mt-0.5 block pl-6 text-xs text-muted md:hidden">
                  {#if r.usage}<span class="sm:hidden">{bytes(r.usage.exclusive)} freed if deleted · </span>{/if}{stamp(r.modified_at)}
                </span>
              {/if}
            </td>
            {#if !above}
              <td class="px-3 py-2 text-right whitespace-nowrap">
                {#if waiting}{@render wait()}{:else}{r.bytes === null ? "–" : bytes(r.bytes)}{/if}
              </td>
              {#if measured}
                <td class="px-3 py-2 text-right whitespace-nowrap max-sm:hidden">
                  {#if waiting}{@render wait()}{:else}{r.usage ? bytes(r.usage.exclusive) : "–"}{/if}
                </td>
                <td class="px-3 py-2 text-right whitespace-nowrap max-md:hidden">
                  {#if waiting}{@render wait()}{:else}{r.usage ? bytes(r.usage.shared) : "–"}{/if}
                </td>
              {/if}
              <td class="px-3 py-2 whitespace-nowrap text-muted max-md:hidden">{stamp(r.modified_at)}</td>
              <td class="px-3 py-2 max-lg:hidden">
                <span class="whitespace-nowrap">{r.owner}:{r.group}</span>
                <span class="ml-1 text-xs text-muted">{r.mode}</span>
              </td>
              <td class="px-3 py-1 text-right">
                <RowMenu name={r.name} items={[{ label: "Delete…", danger: true, onSelect: () => (deleting = r) }]} />
              </td>
            {/if}
          </tr>
        {:else}
          <tr><td colspan="7" class="px-3 py-3 text-muted">This folder is empty.</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
{:else}
  <p class="mt-4 text-muted">Reading the folder…</p>
{/if}

{#if deleting}
  <DeleteEntryDialog path={child(deleting.name)} type={deleting.type} freed={deleting.usage?.exclusive ?? (deleting.type === "file" ? deleting.disk_bytes : null)} onClose={() => (deleting = null)} />
{/if}

{#snippet th(key: BrowseKey, label: string, cls = "")}
  {@const on = sort.key === key}
  <th scope="col" class="px-3 py-2 whitespace-nowrap {cls}" aria-sort={on ? (sort.desc ? "descending" : "ascending") : undefined}>
    <button
      type="button"
      class="label inline-flex items-center gap-1 rounded-sm hover:text-fg focus-visible:outline-2 focus-visible:outline-accent {on ? 'text-fg' : ''}"
      onclick={() => (sort = browseSort(sort, key))}
    >
      {label}
      {#if on && sort.desc}
        <ArrowDown class="size-3" aria-hidden="true" />
      {:else if on}
        <ArrowUp class="size-3" aria-hidden="true" />
      {/if}
    </button>
  </th>
{/snippet}

{#snippet wait()}
  <span class="inline-block h-3 w-12 rounded bg-sunken align-middle motion-safe:animate-pulse" aria-label="Measuring"></span>
{/snippet}
