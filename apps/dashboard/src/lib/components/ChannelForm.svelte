<script lang="ts">
  import { untrack } from "svelte";
  import { api, call, describe } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import { channelDraft, channelInput, channelProblem, DEFAULT_NTFY, EVENT_CATEGORIES, KIND_LABEL, type Channel, type ChannelKind } from "#lib/notifications.ts";

  interface Props {
    /** The channel to change; null adds one. */
    channel: Channel | null;
    onSaved: (c: Channel) => void;
    onClose: () => void;
  }
  let { channel, onSaved, onClose }: Props = $props();

  const id = $props.id();
  const creating = $derived(channel === null);
  /** The editable copy; the form is mounted anew for each channel. */
  let draft = $state(untrack(() => channelDraft(channel)));
  let error = $state("");
  let pending = $state(false);
  const problem = $derived(channelProblem(draft));

  const KINDS: { kind: ChannelKind; about: string }[] = [
    { kind: "ntfy", about: "Push to the ntfy app" },
    { kind: "webhook", about: "JSON to any address" },
  ];

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (problem) return;
    pending = true;
    error = "";
    try {
      const body = channelInput(draft);
      onSaved(
        channel
          ? await call(api.PUT("/notification-channels/{id}", { params: { path: { id: channel.id } }, body }))
          : await call(api.POST("/notification-channels", { body })),
      );
    } catch (err) {
      error = describe(err);
    } finally {
      pending = false;
    }
  }
</script>

<Modal
  open={true}
  onOpenChange={(o) => !o && onClose()}
  title={channel ? `Edit ${channel.name}` : "Add a channel"}
  description={channel ? KIND_LABEL[channel.kind] : "Tokens are stored encrypted on this host."}
>
  <form id="{id}-form" class="grid gap-4" onsubmit={submit}>
    {#if creating}
      <fieldset class="grid gap-2">
        <legend class="mb-1 text-muted">
          <Help label="Kind">
            ntfy pushes to its phone app through a topic. A webhook POSTs each notification as JSON (title, message, severity, event, key, url, sent_at)
            to an address.
          </Help>
        </legend>
        {#each KINDS as k (k.kind)}
          <label class="flex cursor-pointer items-start gap-2 rounded-md border px-3 py-2 {draft.kind === k.kind ? 'border-accent bg-accent-soft' : 'border-line'}">
            <input type="radio" class="mt-0.5 accent-accent" name="{id}-kind" value={k.kind} bind:group={draft.kind} />
            <span>
              {KIND_LABEL[k.kind]}
              <span class="block text-xs text-muted">{k.about}</span>
            </span>
          </label>
        {/each}
      </fieldset>
    {/if}

    <div class="grid gap-1">
      <label for="{id}-name" class="text-muted">Name</label>
      <input id="{id}-name" class="input" autocomplete="off" placeholder={draft.kind === "ntfy" ? "Phone" : "Home Assistant"} bind:value={draft.name} />
    </div>

    {#if draft.kind === "ntfy"}
      <div class="grid gap-1">
        <label for="{id}-topic" class="text-muted">
          <Help label="Topic">The name the app subscribes to. On the public server anyone who knows it can read it, so make it hard to guess.</Help>
        </label>
        <input id="{id}-topic" class="input" autocomplete="off" spellcheck="false" bind:value={draft.topic} />
      </div>
      <div class="grid gap-1">
        <label for="{id}-server" class="text-muted">Server (optional)</label>
        <input id="{id}-server" class="input" autocomplete="off" placeholder={DEFAULT_NTFY} bind:value={draft.server} />
      </div>
    {:else}
      <div class="grid gap-1">
        <label for="{id}-url" class="text-muted">Address</label>
        <input id="{id}-url" class="input" autocomplete="off" spellcheck="false" placeholder="https://example.com/hook" bind:value={draft.url} />
      </div>
      <div class="grid gap-1">
        <label for="{id}-pin" class="text-muted">
          <Help label="Certificate pin (optional)">
            For a receiver with a self-signed certificate: the SHA-256 of the certificate, in hex. Only that certificate is then accepted.
          </Help>
        </label>
        <input id="{id}-pin" class="input" autocomplete="off" spellcheck="false" bind:value={draft.pin} />
      </div>
    {/if}

    <div class="grid gap-1">
      <label for="{id}-token" class="text-muted">
        <Help label="Token (optional)">Sent as a bearer token. For ntfy, an access token for a protected topic.</Help>
      </label>
      <input
        id="{id}-token"
        class="input"
        type="password"
        autocomplete="new-password"
        placeholder={!creating && channel?.has_token ? "Leave empty to keep the stored token" : ""}
        disabled={draft.clearToken}
        bind:value={draft.token}
      />
      {#if !creating && channel?.has_token}
        <label class="mt-1 flex items-center gap-2 text-muted">
          <input type="checkbox" class="accent-accent" bind:checked={draft.clearToken} />
          Remove the stored token
        </label>
      {/if}
    </div>

    <fieldset class="grid gap-2">
      <legend class="mb-1 text-muted">
        <Help label="Send">
          Choose whether info events leave this host too. Open problems and failed recoveries always deliver.
        </Help>
      </legend>
      <div class="grid grid-cols-2 gap-2">
        <label class="flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 {draft.minSeverity === 'all' ? 'border-accent bg-accent-soft' : 'border-line'}">
          <input type="radio" class="accent-accent" name="{id}-severity" value="all" bind:group={draft.minSeverity} />
          <span>Alerts and info</span>
        </label>
        <label class="flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 {draft.minSeverity === 'alert' ? 'border-accent bg-accent-soft' : 'border-line'}">
          <input type="radio" class="accent-accent" name="{id}-severity" value="alert" bind:group={draft.minSeverity} />
          <span>Alerts only</span>
        </label>
      </div>
    </fieldset>

    <fieldset class="grid gap-1.5">
      <legend class="mb-1 text-muted">
        <Help label="Kinds">
          Uncheck a kind to mute its info events and one-off alerts on this channel. Open problems still deliver.
        </Help>
      </legend>
      <div class="flex flex-wrap gap-x-4 gap-y-2">
        {#each EVENT_CATEGORIES as cat (cat.id)}
          {@const checked = !draft.muted.includes(cat.id)}
          <label class="flex cursor-pointer items-center gap-1.5">
            <input
              type="checkbox"
              class="accent-accent"
              {checked}
              onchange={(e) => {
                const on = (e.currentTarget as HTMLInputElement).checked;
                draft.muted = on ? draft.muted.filter((k) => k !== cat.id) : [...draft.muted, cat.id];
              }}
            />
            <span>{cat.label}</span>
          </label>
        {/each}
      </div>
    </fieldset>

    {#if error}
      <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
    {:else if problem}
      <p class="text-muted">{problem}</p>
    {/if}
  </form>
  {#snippet actions()}
    <button type="submit" form="{id}-form" class="btn btn-primary" disabled={!!problem || pending} title={problem}>
      {pending ? "Saving…" : creating ? "Add channel" : "Save"}
    </button>
  {/snippet}
</Modal>
