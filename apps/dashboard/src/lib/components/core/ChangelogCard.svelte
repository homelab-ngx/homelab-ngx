<script lang="ts">
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { releaseBadge, splitChangelog, type ReleaseNote } from "./changelog.ts";
  import type { ImageStatus } from "./core.ts";
  import ReleaseChanges from "./ReleaseChanges.svelte";

  interface Props {
    image: ImageStatus;
    releases: readonly ReleaseNote[];
    loading?: boolean;
  }
  let { image, releases, loading = false }: Props = $props();
  const id = $props.id();

  let earlierOpen = $state(false);
  let earlierEl = $state<HTMLDivElement>();

  const split = $derived(splitChangelog(releases, image));

  function collapseEarlier() {
    earlierOpen = false;
    earlierEl?.scrollIntoView?.({ block: "nearest" });
  }
</script>

{#snippet releaseBlock(r: ReleaseNote)}
  {@const badge = releaseBadge(r.version, image)}
  <div class="py-3 first:pt-0 last:pb-0">
    <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1">
      <span class="font-semibold tabular-nums">{r.version}</span>
      {#if badge}<Pill tone={badge.tone}>{badge.label}</Pill>{/if}
      {#if r.date}<span class="text-xs text-muted tabular-nums">{r.date}</span>{/if}
    </div>
    <div class="mt-2"><ReleaseChanges note={r} /></div>
  </div>
{/snippet}

<section class="card mt-4" aria-labelledby="{id}-h">
  <div class="flex items-center justify-between gap-2">
    <h2 id="{id}-h" class="font-semibold">
      <Help label="Changelog">Short list of user-facing changes for each release. Full notes and package diffs are on GitHub.</Help>
    </h2>
    <div class="flex items-center gap-2">
      {#if releases.length > 0}
        <span class="text-xs text-muted tabular-nums">{releases.length} {releases.length === 1 ? "release" : "releases"}</span>
      {/if}
      {#if earlierOpen}
        <button type="button" class="btn btn-sm" onclick={collapseEarlier}>Collapse</button>
      {/if}
    </div>
  </div>

  {#if loading && releases.length === 0}
    <p class="mt-3 text-muted">Loading…</p>
  {:else if releases.length === 0}
    <p class="mt-3 text-muted">No release notes yet.</p>
  {:else}
    <div class="mt-3 divide-y divide-line">
      {#each split.featured as r (r.version)}
        {@render releaseBlock(r)}
      {/each}
    </div>
    {#if split.earlier.length > 0}
      <div bind:this={earlierEl} class="mt-3 border-t border-line pt-3">
        <Disclosure
          label="Earlier releases"
          summary="{split.earlier.length} {split.earlier.length === 1 ? 'version' : 'versions'}"
          bind:open={earlierOpen}
        >
          <div class="divide-y divide-line">
            {#each split.earlier as r (r.version)}
              {@render releaseBlock(r)}
            {/each}
          </div>
          {#if earlierOpen}
            <div class="mt-3 flex justify-end border-t border-line pt-3">
              <button type="button" class="btn btn-sm" onclick={collapseEarlier}>Collapse</button>
            </div>
          {/if}
        </Disclosure>
      </div>
    {/if}
  {/if}
</section>
