<script lang="ts">
  import { createInfiniteQuery, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import AskButton from "#lib/components/ask/AskButton.svelte";
  import ChannelForm from "#lib/components/ChannelForm.svelte";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill, { type Tone } from "#lib/components/Pill.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import { ago, stamp } from "#lib/format.ts";
  import { channelDraft, channelInput, channelKeys, channelState, channelWhere, KIND_LABEL, type Channel } from "#lib/notifications.ts";

  type Snapshot = Schema<"NotificationsSnapshot">;
  type Entry = Schema<"NotificationLogEntry">;

  const PAGE = 50;
  const client = useQueryClient();

  const channels = createQuery(() => ({
    queryKey: channelKeys.list,
    queryFn: () => call<Channel[]>(api.GET("/notification-channels")),
  }));
  const chans = $derived(channels.data ?? []);
  /** The channel form: a channel to edit, or null to add one. */
  let form = $state<{ channel: Channel | null } | null>(null);
  let removing = $state<Channel | null>(null);
  let actionError = $state("");
  let tests = $state<Record<number, Schema<"ChannelTest"> | "running">>({});
  const at = (id: number) => ({ params: { path: { id } } });

  async function refresh() {
    await client.invalidateQueries({ queryKey: ["daemon", "notifications"] });
  }
  async function act(run: () => Promise<unknown>) {
    actionError = "";
    try {
      await run();
    } catch (err) {
      actionError = describe(err);
    }
    await refresh();
  }
  async function test(c: Channel) {
    tests[c.id] = "running";
    try {
      tests[c.id] = await call(api.POST("/notification-channels/{id}/test", at(c.id)));
    } catch (err) {
      tests[c.id] = { ok: false, error: describe(err) };
    }
  }
  const toggle = (c: Channel) =>
    act(() => call(api.PUT("/notification-channels/{id}", { ...at(c.id), body: { ...channelInput(channelDraft(c)), enabled: !c.enabled } })));

  const snapshot = createQuery(() => ({
    queryKey: ["daemon", "notifications"],
    queryFn: () => call<Snapshot>(api.GET("/notifications")),
    refetchInterval: 30_000,
  }));
  /** The persisted log (90 days), newest first, paged by id. */
  const log = createInfiniteQuery(() => ({
    queryKey: ["daemon", "notifications", "log"],
    queryFn: ({ pageParam }: { pageParam: number | undefined }) =>
      call<Entry[]>(api.GET("/notifications/log", { params: { query: { before: pageParam, limit: PAGE } } })),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (page: Entry[]) => (page.length === PAGE ? page.at(-1)?.id : undefined),
    refetchInterval: 30_000,
  }));

  const failing = $derived(
    Object.entries(snapshot.data?.failing_channels ?? {})
      .map(([name, f]) => ({ name, ...f }))
      .sort((a, b) => a.since.localeCompare(b.since)),
  );
  const open = $derived(
    Object.entries(snapshot.data?.open ?? {})
      .map(([key, p]) => ({ key, ...p }))
      .sort((a, b) => a.opened_at.localeCompare(b.opened_at)),
  );
  const entries = $derived(log.data?.pages.flat() ?? []);
  /** An open problem's newest log entry, which the assistant reads. */
  const entryOf = (key: string) => entries.find((e) => e.key === key);

  /** A history row's mark: a problem opening or coming back, its end, or a plain message. */
  function kindTone(e: Entry): Tone {
    if (e.kind === "resolved") return "ok";
    if (e.kind === "sent") return e.severity === "alert" ? "warn" : "idle";
    return e.kind === "reminded" ? "warn" : "bad";
  }

  /** What went wrong delivering an entry, or "" when every channel delivered. */
  function delivery(e: Entry): string {
    if (e.deliveries.length === 0) return "not sent";
    return e.deliveries
      .filter((d) => !d.ok)
      .map((d) => `${d.channel} failed`)
      .join(", ");
  }

  /** The body's first line, without a title it repeats. */
  function detail(e: Entry): string {
    const first = e.body.split("\n", 1)[0]?.trim() ?? "";
    if (first === e.title) return "";
    const prefix = `${e.title} · `;
    return first.startsWith(prefix) ? first.slice(prefix.length) : first;
  }
</script>

<svelte:head><title>Notifications · Homelab-ngx</title></svelte:head>

<h1 class="text-xl font-semibold">Notifications</h1>
<p class="mt-0.5 text-muted">
  {open.length} open · {failing.length === 0 ? "delivery healthy" : `${failing.length} channel${failing.length === 1 ? "" : "s"} failing`}
</p>

{#if snapshot.error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(snapshot.error)}</p>
{/if}

<div class="mt-5 flex flex-col gap-4">
  {#if failing.length > 0}
    <section class="card" aria-labelledby="failing-h">
      <h2 id="failing-h" class="font-semibold">Delivery</h2>
      <ul class="mt-2 flex flex-col gap-1.5">
        {#each failing as f (f.name)}
          <li class="flex flex-wrap items-baseline gap-x-2">
            <Pill tone="bad">failing</Pill>
            <span>{f.name}</span>
            <span class="text-xs text-muted">since {stamp(f.since)}</span>
            <span class="basis-full text-xs break-words text-muted">{f.error}</span>
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  <section class="card" aria-labelledby="channels-h">
    <div class="flex flex-wrap items-baseline gap-2">
      <h2 id="channels-h" class="min-w-0 flex-1 font-semibold">
        <Help label="Channels">Every channel that is on gets every notification. One that takes it is enough; a failing one shows here.</Help>
      </h2>
      <button type="button" class="btn btn-primary" onclick={() => (form = { channel: null })}>Add channel</button>
    </div>
    {#if channels.error}
      <p class="mt-1 text-bad">{describe(channels.error)}</p>
    {:else if channels.data && chans.length === 0}
      <p class="mt-1 text-muted">No channels. Notifications are off.</p>
    {:else}
      <ul class="mt-2 divide-y divide-line">
        {#each chans as c (c.id)}
          {@const t = tests[c.id]}
          {@const st = channelState(c, !!snapshot.data?.failing_channels[c.name], t)}
          <li class="flex flex-wrap items-center gap-x-3 gap-y-1 py-1.5">
            <span class="min-w-0 flex-1">
              <span class="block truncate">{c.name}</span>
              <span class="block truncate text-xs text-muted">{KIND_LABEL[c.kind]} · {channelWhere(c)}</span>
              {#if t && t !== "running" && !t.ok}<span class="block text-xs break-words text-bad">{t.error}</span>{/if}
            </span>
            <span class="w-24 shrink-0"><Pill tone={st.tone}>{st.label}</Pill></span>
            <button type="button" class="btn btn-sm" disabled={t === "running"} onclick={() => test(c)}>Test</button>
            <RowMenu
              name={c.name}
              items={[
                { label: "Edit", onSelect: () => (form = { channel: c }) },
                { label: c.enabled ? "Turn off" : "Turn on", onSelect: () => toggle(c) },
                { label: "Remove…", danger: true, onSelect: () => (removing = c) },
              ]}
            />
          </li>
        {/each}
      </ul>
    {/if}
    {#if actionError}<p role="alert" class="mt-2 rounded-md bg-bad-soft px-3 py-2 text-bad">{actionError}</p>{/if}
  </section>

  <section class="card" aria-labelledby="open-h">
    <h2 id="open-h" class="font-semibold">Open problems</h2>
    {#if open.length === 0}
      <p class="mt-1 text-muted">Nothing open.</p>
    {:else}
      <ul class="mt-2 flex flex-col gap-1.5">
        {#each open as p (p.key)}
          <li class="flex items-center gap-2">
            <Pill tone="bad">open</Pill>
            <span class="min-w-0 flex-1 truncate">{p.title}</span>
            <span class="shrink-0 text-xs text-muted" title={stamp(p.opened_at)}>since {ago(p.opened_at)}</span>
            {#if entryOf(p.key)}
              <AskButton small title={p.title} subject={{ kind: "notification", ref: String(entryOf(p.key)!.id) }} />
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section class="card" aria-labelledby="log-h">
    <h2 id="log-h" class="font-semibold">History</h2>
    {#if log.error}
      <p class="mt-1 text-bad">{describe(log.error)}</p>
    {:else if log.isPending}
      <p class="mt-1 text-muted">Loading…</p>
    {:else if entries.length === 0}
      <p class="mt-1 text-muted">No notifications in the last 90 days.</p>
    {:else}
      <ol class="mt-2 divide-y divide-line">
        {#each entries as e (e.id)}
          {@const failed = delivery(e)}
          {@const more = detail(e)}
          <li class="flex items-baseline gap-3 py-1.5">
            <time datetime={e.at} class="w-28 shrink-0 text-xs text-muted">{stamp(e.at)}</time>
            <span class="min-w-0 flex-1">
              <span class="block truncate">{e.title}</span>
              {#if failed || more}
                <span class="block truncate text-xs text-muted">
                  {#if failed}<span class={failed === "not sent" ? "" : "text-bad"}>{failed}</span>{/if}{#if failed && more}&nbsp;·&nbsp;{/if}{more}
                </span>
              {/if}
            </span>
            <span class="w-24 shrink-0"><Pill tone={kindTone(e)}>{e.kind}</Pill></span>
            <span class="w-14 shrink-0 text-right">
              {#if kindTone(e) === "bad" || kindTone(e) === "warn"}
                <AskButton small title={e.title} subject={{ kind: "notification", ref: String(e.id) }} />
              {/if}
            </span>
          </li>
        {/each}
      </ol>
      {#if log.hasNextPage}
        <button type="button" class="btn btn-sm mt-3" disabled={log.isFetchingNextPage} onclick={() => log.fetchNextPage()}>
          {log.isFetchingNextPage ? "Loading…" : "Older"}
        </button>
      {/if}
    {/if}
  </section>
</div>

{#if form}
  <ChannelForm
    channel={form.channel}
    onSaved={() => {
      form = null;
      refresh();
    }}
    onClose={() => (form = null)}
  />
{/if}

{#if removing}
  {@const c = removing}
  <Modal open={true} onOpenChange={(o) => !o && (removing = null)} title="Remove {c.name}?" description="Notifications stop going there. The history stays.">
    {#snippet children()}{/snippet}
    {#snippet actions()}
      <button
        type="button"
        class="btn btn-danger"
        onclick={() => {
          removing = null;
          act(() => call(api.DELETE("/notification-channels/{id}", at(c.id))));
        }}>Remove</button
      >
    {/snippet}
  </Modal>
{/if}
