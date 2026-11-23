<script lang="ts">
  import { page } from "$app/state";
  import HostEditor from "#lib/components/files/HostEditor.svelte";
  import { browseHref, parentOf, relativeTo } from "#lib/storage.ts";

  const path = $derived(page.url.searchParams.get("path") ?? "");
  /** The file list starts at ?root= when the file is below it, else at the file's own folder. */
  const root = $derived.by(() => {
    const r = page.url.searchParams.get("root");
    return r && relativeTo(r, path) !== null ? r : parentOf(path);
  });
</script>

<svelte:head><title>{path || "Files"} · Storage · Homelab-ngx</title></svelte:head>

{#if path.startsWith("/")}
  <!-- Opening another file keeps the list; a new root starts it again. -->
  {#key root}
    <HostEditor {root} {path} />
  {/key}
{:else}
  <a class="link text-xs" href={browseHref("/")}>← Files</a>
  <p class="mt-4 text-muted">No file to open.</p>
{/if}
