<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { api, call, describe, ProblemError } from "#lib/api/client.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { bytes, duration, stamp } from "#lib/format.ts";
  import { backupKeys, byDestination, ITEM_STATE, RUN_STATE, type Preview, type PreviewItem } from "#lib/backups.ts";

  interface Props {
    backupId: number;
    /** The backup has a preview to read; without one nothing is asked for or shown. */
    has: boolean;
    /** A preview of this backup is in flight: read it until it ends. */
    previewing: boolean;
  }
  let { backupId, has, previewing }: Props = $props();

  // A run removes the preview: an answer of 404 is "nothing to show".
  const preview = createQuery(() => ({
    queryKey: backupKeys.preview(backupId),
    queryFn: async (): Promise<Preview | null> => {
      try {
        return await call(api.GET("/backups/{id}/preview", { params: { path: { id: backupId } } }));
      } catch (e) {
        if (e instanceof ProblemError && e.code === "no_preview") return null;
        throw e;
      }
    },
    enabled: has || previewing,
    refetchInterval: (q) => (previewing || q.state.data?.result === "running" ? 2_500 : false),
  }));
  const p = $derived(preview.data ?? null);
  const groups = $derived(byDestination(p?.items ?? []));

  const count = (n: number, size: number) => (n > 0 ? `${n} · ${bytes(size)}` : "0");
  /** The paths worth reading: what a run would send or remove. */
  const samples = (it: PreviewItem) =>
    [
      { label: "New", total: it.new, paths: it.new_sample },
      { label: "Changed", total: it.changed, paths: it.changed_sample },
      { label: it.deletes ? "Deleted" : "Kept", total: it.extra, paths: it.extra_sample },
    ].filter((s) => s.paths.length > 0);
</script>

{#if preview.error}
  <p class="text-bad">{describe(preview.error)}</p>
{:else if p && (has || previewing)}
  {@const st = RUN_STATE[p.result]}
  <section class="mt-5" aria-labelledby="preview-heading">
    <h2 id="preview-heading" class="font-semibold">Preview</h2>
    <div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1">
      <Pill tone={st.tone}>{p.result === "success" ? "Done" : st.label}</Pill>
      <p class="text-muted tabular-nums">{stamp(p.started_at)} · {duration(p.started_at, p.finished_at)} · Nothing copied</p>
    </div>
    {#if p.error && p.result !== "cancelled"}
      <p class="mt-2 rounded-md bg-bad-soft px-3 py-2 break-words text-bad">{p.error}</p>
    {/if}
    {#if p.result === "running"}
      <div class="mt-3 rounded-md border border-line px-3 py-2" role="status">
        {#if p.progress}
          <p>Comparing {p.progress.folder} with {p.progress.destination}</p>
          <div class="mt-1 h-1.5 overflow-hidden rounded-full bg-sunken">
            <div class="h-full w-1/4 rounded-full bg-accent motion-safe:animate-pulse"></div>
          </div>
          <p class="mt-1 text-xs text-muted tabular-nums">{p.progress.files_done} files compared</p>
        {:else}
          <p>Getting ready…</p>
        {/if}
      </div>
    {/if}

    {#each groups as g (g.name)}
      <section class="mt-4" aria-label={g.name}>
        <div class="flex flex-wrap items-center gap-2">
          <h3 class="font-semibold">{g.name}</h3>
          {#if g.destination && g.destination.result !== "success"}
            {@const ds = ITEM_STATE[g.destination.result]}
            <Pill tone={ds.tone}>{ds.label}</Pill>
          {/if}
        </div>
        {#if g.destination?.error}
          <p class="mt-1 break-words text-bad">{g.destination.error}</p>
        {:else if g.destination?.note}
          <p class="mt-1 text-muted">{g.destination.note}</p>
        {/if}
        {#if g.folders.some((it) => it.result !== "skipped")}
          <!-- Known once a folder is compared; until then the column has no name. -->
          {@const compared = g.folders.filter((it) => it.result === "success" || it.result === "partial")}
          {@const extra = compared.length === 0 ? "–" : compared.some((it) => it.deletes) ? "Deleted" : "Kept"}
          <div class="mt-2 overflow-x-auto rounded-lg border border-line">
            <table class="w-full border-collapse text-left">
              <thead>
                <tr class="border-b border-line bg-sunken">
                  <th scope="col" class="label px-3 py-1.5">Folder</th>
                  <th scope="col" class="label px-3 py-1.5 text-right">New</th>
                  <th scope="col" class="label px-3 py-1.5 text-right">Changed</th>
                  <th scope="col" class="label px-3 py-1.5 text-right max-sm:hidden">Unchanged</th>
                  <th scope="col" class="label px-3 py-1.5 text-right">{extra}</th>
                </tr>
              </thead>
              <tbody>
                {#each g.folders as it (it.label + it.path)}
                  {@const is = ITEM_STATE[it.result]}
                  <tr class="border-b border-line align-baseline last:border-0">
                    <td class="px-3 py-1.5">
                      {#if it.label === it.path}
                        <span class="break-all">{it.path}</span>
                      {:else}
                        {it.label}
                        <div class="text-xs break-all text-muted">{it.path}</div>
                      {/if}
                      {#if it.result !== "success"}<div class="mt-0.5"><Pill tone={is.tone}>{is.label}</Pill></div>{/if}
                      {#if it.error}<div class="mt-0.5 text-xs break-words text-bad">{it.error}</div>{/if}
                      {#if it.note}<div class="mt-0.5 text-xs text-muted">{it.note}</div>{/if}
                      {#if it.errors > 0}<div class="mt-0.5 text-xs text-warn">{it.errors} not compared</div>{/if}
                      {#if samples(it).length > 0}
                        <Disclosure label="Files" class="mt-1">
                          {#each samples(it) as s (s.label)}
                            <p class="label mt-2">{s.label} · {s.paths.length} of {s.total}</p>
                            <ul class="mt-1 text-xs break-all text-muted">
                              {#each s.paths as path (path)}<li>{path}</li>{/each}
                            </ul>
                          {/each}
                        </Disclosure>
                      {/if}
                    </td>
                    <td class="px-3 py-1.5 text-right whitespace-nowrap tabular-nums">{count(it.new, it.new_bytes)}</td>
                    <td class="px-3 py-1.5 text-right whitespace-nowrap tabular-nums">{count(it.changed, it.changed_bytes)}</td>
                    <td class="px-3 py-1.5 text-right tabular-nums max-sm:hidden">{it.unchanged}</td>
                    <td class="px-3 py-1.5 text-right tabular-nums">{it.extra}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </section>
    {/each}
  </section>
{/if}
