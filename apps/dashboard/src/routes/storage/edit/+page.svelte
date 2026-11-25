<script lang="ts">
  import { page } from "$app/state";
  import HostEditor from "#lib/components/files/HostEditor.svelte";
  import { browseHref, editTarget } from "#lib/storage.ts";

  const { path, root } = $derived(editTarget(page.url));
</script>

<svelte:head><title>{path || "Files"} · Storage · Homelab-ngx</title></svelte:head>

{#if path.startsWith("/")}
  <!-- Opening another file keeps the list; a new root starts it again. -->
  {#key root}
    <HostEditor {root} {path} />
  {/key}
{:else}
  <p class="text-muted"><a class="link" href="/storage">Storage</a> / <a class="link" href={browseHref("/")}>Files</a> /</p>
  <p class="mt-4 text-muted">No file to open.</p>
{/if}
