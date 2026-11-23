<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import ChannelForm from "#lib/components/ChannelForm.svelte";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import RowMenu from "#lib/components/RowMenu.svelte";
  import { stamp } from "#lib/format.ts";
  import {
    channelDraft,
    channelInput,
    channelKeys,
    channelState,
    channelWhere,
    failingOf,
    KIND_LABEL,
    notificationKeys,
    type Channel,
    type Snapshot,
  } from "#lib/notifications.ts";

  const client = useQueryClient();

  const channels = createQuery(() => ({
    queryKey: channelKeys.list,
    queryFn: () => call<Channel[]>(api.GET("/notification-channels")),
  }));
  const chans = $derived(channels.data ?? []);
  // The same query as the header's bell: a channel that starts failing shows here and there.
  const snapshot = createQuery(() => ({
    queryKey: notificationKeys.snapshot,
    queryFn: () => call<Snapshot>(api.GET("/notifications")),
    refetchInterval: 30_000,
  }));
  const failing = $derived(failingOf(snapshot.data));

  /** The channel form: a channel to edit, or null to add one. */
  let form = $state<{ channel: Channel | null } | null>(null);
  let removing = $state<Channel | null>(null);
  let actionError = $state("");
  let tests = $state<Record<number, Schema<"ChannelTest"> | "running">>({});
  const at = (id: number) => ({ params: { path: { id } } });

  async function refresh() {
    await client.invalidateQueries({ queryKey: notificationKeys.snapshot });
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
</script>

<svelte:head><title>Notifications · System Settings · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/settings">System Settings</a> /</p>
<h1 class="mt-1 text-xl font-semibold">
  <Help label="Notifications">
    Where alerts go when something needs attention. The bell in the header shows what is open and the history.
  </Help>
</h1>

<div class="mt-5 grid max-w-4xl gap-4">
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
