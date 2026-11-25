<script lang="ts">
  import { ChevronDown, X } from "@lucide/svelte";
  import { createInfiniteQuery } from "@tanstack/svelte-query";
  import { Dialog } from "bits-ui";
  import { api, call, describe } from "#lib/api/client.ts";
  import AskButton from "#lib/components/ask/AskButton.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { ago, stamp } from "#lib/format.ts";
  import { delivery, detail, failingOf, kindTone, LOG_PAGE, notificationHref, notificationKeys, openOf, type Entry, type Snapshot } from "#lib/notifications.ts";

  interface Props {
    /** The header's snapshot query data: the tray shows what the bell counts. */
    snapshot: Snapshot | undefined;
    onClose: () => void;
  }
  let { snapshot, onClose }: Props = $props();

  const failing = $derived(failingOf(snapshot));
  const open = $derived(openOf(snapshot));
  let expandedOpen = $state<Record<string, boolean>>({});
  let expandedLog = $state<Record<number, boolean>>({});

  /** The persisted log (90 days), newest first, paged by id. It is read only while the tray is open. */
  const log = createInfiniteQuery(() => ({
    queryKey: notificationKeys.log,
    queryFn: ({ pageParam }: { pageParam: number | undefined }) =>
      call<Entry[]>(api.GET("/notifications/log", { params: { query: { before: pageParam, limit: LOG_PAGE } } })),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (page: Entry[]) => (page.length === LOG_PAGE ? page.at(-1)?.id : undefined),
    refetchInterval: 30_000,
  }));
  const entries = $derived(log.data?.pages.flat() ?? []);
  /** An open problem's newest log entry, which the assistant reads. */
  const entryOf = (key: string) => entries.find((e) => e.key === key);
</script>

<Dialog.Root open={true} onOpenChange={(o) => !o && onClose()}>
  <Dialog.Portal>
    <Dialog.Overlay class="fixed inset-0 z-40 bg-black/50" />
    <Dialog.Content
      class="fixed inset-y-0 right-0 z-50 flex w-full flex-col border-l border-line bg-surface shadow-xl sm:max-w-md motion-safe:animate-sheet-in"
    >
      <div class="flex items-center gap-2 border-b border-line px-4 py-3">
        <Dialog.Title class="min-w-0 flex-1 text-base font-semibold">Notifications</Dialog.Title>
        <Dialog.Close class="rounded-md p-1 text-muted hover:bg-sunken hover:text-fg focus-visible:outline-2 focus-visible:outline-accent" aria-label="Close">
          <X class="size-4" aria-hidden="true" />
        </Dialog.Close>
      </div>

      <div class="min-h-0 flex-1 overflow-y-auto">
        {#if failing.length > 0}
          <section aria-labelledby="tray-failing-h">
            <h2 id="tray-failing-h" class="label bg-sunken px-4 py-2">Delivery</h2>
            <ul class="divide-y divide-line border-y border-line">
              {#each failing as f (f.name)}
                <li class="flex flex-wrap items-baseline gap-x-2 px-4 py-2">
                  <Pill tone="bad">failing</Pill>
                  <span class="min-w-0 flex-1 truncate">{f.name}</span>
                  <span class="text-xs text-muted">since {stamp(f.since)}</span>
                  <span class="basis-full text-xs break-words text-muted">{f.error}</span>
                </li>
              {/each}
            </ul>
            <p class="px-4 py-2 text-right"><a class="link text-xs" href="/settings/notifications" onclick={onClose}>Fix in Settings</a></p>
          </section>
        {/if}

        <section aria-labelledby="tray-open-h">
          <h2 id="tray-open-h" class="label bg-sunken px-4 py-2">Open</h2>
          {#if open.length === 0}
            <p class="border-t border-line px-4 py-2 text-muted">Nothing open.</p>
          {:else}
            <ul class="divide-y divide-line border-t border-line">
              {#each open as p (p.key)}
                {@const href = notificationHref(p.key, p.title)}
                {@const entry = entryOf(p.key)}
                {@const expanded = !!expandedOpen[p.key]}
                <li class="px-4 py-2">
                  <div class="flex items-center gap-2">
                    <Pill tone="bad">open</Pill>
                    {#if href}
                      <a class="link min-w-0 flex-1 {expanded ? 'break-words' : 'truncate'}" {href} onclick={onClose}>{p.title}</a>
                    {:else}
                      <span class="min-w-0 flex-1 {expanded ? 'break-words' : 'truncate'}">{p.title}</span>
                    {/if}
                    <span class="shrink-0 text-xs text-muted" title={stamp(p.opened_at)}>since {ago(p.opened_at)}</span>
                    <button
                      type="button"
                      class="btn btn-sm px-1.5"
                      aria-expanded={expanded}
                      aria-label={expanded ? "Collapse notification" : "Expand notification"}
                      onclick={() => (expandedOpen[p.key] = !expanded)}
                    >
                      <ChevronDown class="size-3.5 transition-transform {expanded ? 'rotate-180' : ''}" aria-hidden="true" />
                    </button>
                    {#if entry}
                      <AskButton small title={p.title} subject={{ kind: "notification", ref: String(entry.id) }} />
                    {/if}
                  </div>
                  {#if expanded}
                    <div class="mt-2 rounded-md bg-sunken px-2.5 py-2 text-xs whitespace-pre-wrap break-words text-muted">
                      <p class="text-fg">{p.title}</p>
                      {#if entry?.body && entry.body.trim() !== p.title.trim()}
                        <p class="mt-1">{entry.body}</p>
                      {/if}
                    </div>
                  {/if}
                </li>
              {/each}
            </ul>
          {/if}
        </section>

        <section aria-labelledby="tray-log-h">
          <h2 id="tray-log-h" class="label border-t border-line bg-sunken px-4 py-2">History</h2>
          {#if log.error}
            <p class="border-t border-line px-4 py-2 text-bad">{describe(log.error)}</p>
          {:else if log.isPending}
            <p class="border-t border-line px-4 py-2 text-muted">Loading…</p>
          {:else if entries.length === 0}
            <p class="border-t border-line px-4 py-2 text-muted">No notifications in the last 90 days.</p>
          {:else}
            <ol class="divide-y divide-line border-t border-line">
              {#each entries as e (e.id)}
                {@const failed = delivery(e)}
                {@const more = detail(e)}
                {@const href = notificationHref(e.key, e.title)}
                {@const expanded = !!expandedLog[e.id]}
                {@const hasBody = Boolean(e.body.trim() && e.body.trim() !== e.title.trim())}
                {@const errors = e.deliveries.filter((d) => !d.ok && d.error)}
                <li class="px-4 py-2">
                  <div class="flex items-start gap-2">
                    <button
                      type="button"
                      class="min-w-0 flex-1 rounded-sm text-left focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                      aria-expanded={expanded}
                      onclick={() => (expandedLog[e.id] = !expanded)}
                    >
                      <span class="block {expanded ? 'font-medium break-words' : 'truncate'}">{e.title}</span>
                      <span class="block text-xs text-muted {expanded ? 'break-words' : 'truncate'}">
                        <time datetime={e.at}>{stamp(e.at)}</time>{#if failed}&nbsp;·&nbsp;<span class={failed === "not sent" ? "" : "text-bad"}>{failed}</span>{/if}{#if !expanded && more}&nbsp;·&nbsp;{more}{/if}
                      </span>
                    </button>
                    <span class="w-20 shrink-0"><Pill tone={kindTone(e)}>{e.kind}</Pill></span>
                    {#if href}
                      <a class="link shrink-0 text-xs" {href} onclick={onClose}>Open</a>
                    {/if}
                    {#if kindTone(e) === "bad" || kindTone(e) === "warn"}
                      <AskButton small title={e.title} subject={{ kind: "notification", ref: String(e.id) }} />
                    {/if}
                  </div>
                  {#if expanded && (hasBody || errors.length > 0)}
                    <div class="mt-2 rounded-md bg-sunken px-2.5 py-2 text-xs whitespace-pre-wrap break-words text-muted">
                      {#if hasBody}<p>{e.body}</p>{/if}
                      {#each errors as d (d.channel)}
                        <p class="mt-1 text-bad">{d.channel}: {d.error}</p>
                      {/each}
                    </div>
                  {/if}
                </li>
              {/each}
            </ol>
            {#if log.hasNextPage}
              <p class="px-4 py-3">
                <button type="button" class="btn btn-sm" disabled={log.isFetchingNextPage} onclick={() => log.fetchNextPage()}>
                  {log.isFetchingNextPage ? "Loading…" : "Older"}
                </button>
              </p>
            {/if}
          {/if}
        </section>
      </div>
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
