<script lang="ts">
  import { untrack } from "svelte";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import {
    destinationDraft,
    destinationInput,
    destinationProblem,
    describeKeep,
    KEEP_PRESETS,
    KIND_LABEL,
    presetOf,
    type Destination,
    type KeepPreset,
  } from "#lib/backups.ts";

  interface Props {
    /** The destination to change; null adds one. */
    destination: Destination | null;
    onSaved: (created: Schema<"DestinationCreated">) => void;
    onClose: () => void;
  }
  let { destination, onSaved, onClose }: Props = $props();

  const id = $props.id();
  const creating = $derived(destination === null);
  /** The editable copy; the form is mounted anew for each destination. */
  let draft = $state(untrack(() => destinationDraft(destination)));
  let error = $state("");
  let pending = $state(false);

  const problem = $derived(destinationProblem(draft, creating));
  const preset = $derived(presetOf(draft.keep));
  /** The numbers show from the start when no preset has them. */
  let keepOpen = $state(untrack(() => presetOf(draft.keep) === "custom"));

  const KINDS: { kind: Destination["kind"]; about: string }[] = [
    { kind: "s3", about: "Backblaze B2, Cloudflare R2, Wasabi, AWS, MinIO" },
    { kind: "restic", about: "Encrypted snapshots with history" },
    { kind: "rclone", about: "Google Drive, SFTP, a NAS share and more" },
  ];

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (problem) return;
    pending = true;
    error = "";
    try {
      const body = destinationInput(draft);
      if (destination) {
        const d = await call(api.PUT("/backup-destinations/{id}", { params: { path: { id: destination.id } }, body }));
        onSaved({ destination: d });
      } else {
        onSaved(await call(api.POST("/backup-destinations", { body })));
      }
    } catch (err) {
      error = describe(err);
    } finally {
      pending = false;
    }
  }
</script>

{#snippet deleteSwitch()}
  <div class="flex flex-wrap items-center gap-x-2">
    <input type="checkbox" class="accent-accent" aria-label="Delete removed and skipped files" bind:checked={draft.deleteRemoved} />
    <Help label="Delete removed and skipped files">
      When on, a file deleted on the host, or left out by a skip pattern, is deleted here on the next run. A file deleted by mistake is then gone here
      too, unless the storage keeps old versions. When off, such files stay.
    </Help>
  </div>
{/snippet}

<Modal
  open={true}
  onOpenChange={(o) => !o && onClose()}
  title={destination ? `Edit ${destination.name}` : "Add a destination"}
  description={destination ? KIND_LABEL[destination.kind] : "Keys are stored encrypted on this host."}
>
  <form id="{id}-form" class="grid gap-4" onsubmit={submit}>
    {#if creating}
      <fieldset class="grid gap-2">
        <legend class="mb-1 text-muted">
          <Help label="Kind">S3 and rclone copy files as they are. restic keeps encrypted snapshots, so an earlier day can be restored.</Help>
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
      <input id="{id}-name" class="input" autocomplete="off" placeholder="Backblaze" bind:value={draft.name} />
    </div>

    {#if draft.kind === "s3"}
      <div class="grid gap-1">
        <label for="{id}-endpoint" class="text-muted">
          <Help label="Endpoint">The address of the storage provider's S3 service, found on the bucket's page or in the provider's documentation.</Help>
        </label>
        <input id="{id}-endpoint" class="input" autocomplete="off" placeholder="https://s3.eu-central-003.backblazeb2.com" bind:value={draft.endpoint} />
      </div>
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="grid gap-1">
          <label for="{id}-bucket" class="text-muted">Bucket</label>
          <input id="{id}-bucket" class="input" autocomplete="off" bind:value={draft.bucket} />
        </div>
        <div class="grid gap-1">
          <label for="{id}-prefix" class="text-muted">Folder in the bucket (optional)</label>
          <input id="{id}-prefix" class="input" autocomplete="off" bind:value={draft.prefix} />
        </div>
        <div class="grid gap-1">
          <label for="{id}-region" class="text-muted">Region (optional)</label>
          <input id="{id}-region" class="input" autocomplete="off" bind:value={draft.region} />
        </div>
        <div class="grid gap-1">
          <label for="{id}-akid" class="text-muted">Access key id</label>
          <input id="{id}-akid" class="input" autocomplete="off" bind:value={draft.accessKeyId} />
        </div>
      </div>
      <div class="grid gap-1">
        <label for="{id}-secret" class="text-muted">Secret access key</label>
        <input id="{id}-secret" class="input" type="password" autocomplete="new-password" placeholder={creating ? "" : "Leave empty to keep the stored key"} bind:value={draft.secretAccessKey} />
      </div>
      {@render deleteSwitch()}
      <p class="text-xs text-muted">Earlier versions of changed files are kept only if the bucket has versioning on.</p>
    {:else if draft.kind === "rclone"}
      <div class="grid gap-1">
        <label for="{id}-config" class="text-muted">
          <Help label="Remote config">
            One remote's section from an rclone config file: its name in square brackets, then its settings. rclone config makes one on any computer;
            rclone config show prints it.
          </Help>
        </label>
        <textarea
          id="{id}-config"
          class="input min-h-28"
          spellcheck="false"
          autocomplete="off"
          placeholder={creating ? "[mydrive]\ntype = drive\ntoken = …" : "Leave empty to keep the stored config"}
          bind:value={draft.rcloneConfig}
        ></textarea>
      </div>
      <div class="grid gap-1">
        <label for="{id}-path" class="text-muted">Folder on the remote</label>
        <input id="{id}-path" class="input" autocomplete="off" placeholder="backups/homelab" bind:value={draft.rclonePath} />
      </div>
      {@render deleteSwitch()}
    {:else}
      <div class="grid gap-1">
        <label for="{id}-repo" class="text-muted">
          <Help label="Repository">
            Where restic keeps the snapshots: a folder such as /mnt/usb/backups, or an address such as s3:https://host/bucket/folder or b2:bucket:folder.
            Passwords go in the credentials below, not in the address.
          </Help>
        </label>
        <input id="{id}-repo" class="input" autocomplete="off" placeholder="s3:https://s3.example.com/bucket/homelab" bind:value={draft.repository} />
      </div>

      <fieldset class="grid gap-2">
        <legend class="mb-1 text-muted">
          <Help label="Storage credentials">
            The keys the repository's storage asks for, under restic's names: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY for S3, B2_ACCOUNT_ID and
            B2_ACCOUNT_KEY for Backblaze. A folder on a disk needs none.
          </Help>
        </legend>
        {#if draft.env === null}
          <p>
            {#if destination?.restic?.env_names.length}
              Stored: {destination.restic.env_names.join(", ")}
            {:else}
              None stored.
            {/if}
          </p>
          <div><button type="button" class="btn btn-sm" onclick={() => (draft.env = [{ name: "", value: "" }])}>Replace</button></div>
        {:else}
          {#each draft.env as row, i (i)}
            <div class="flex items-center gap-1.5">
              <input class="input min-w-0 flex-1" aria-label="Credential name" autocomplete="off" placeholder="AWS_ACCESS_KEY_ID" bind:value={row.name} />
              <input class="input min-w-0 flex-1" aria-label="Value of {row.name || 'the credential'}" type="password" autocomplete="new-password" bind:value={row.value} />
              <button type="button" class="btn btn-sm" aria-label="Remove {row.name || 'this credential'}" onclick={() => draft.env?.splice(i, 1)}>Remove</button>
            </div>
          {/each}
          <div><button type="button" class="btn btn-sm" onclick={() => draft.env?.push({ name: "", value: "" })}>Add a credential</button></div>
        {/if}
      </fieldset>

      {#if creating}
        <div class="grid gap-2">
          <div class="flex flex-wrap items-center gap-x-2">
            <input type="checkbox" class="accent-accent" aria-label="Existing repository" bind:checked={draft.existing} />
            <Help label="Existing repository">
              Tick for a repository that already holds snapshots, and give its password. For a new one the host generates a strong password and shows
              it to be saved.
            </Help>
          </div>
          <div class="grid gap-1">
            <label for="{id}-password" class="text-muted">Repository password</label>
            <input
              id="{id}-password"
              class="input"
              type="password"
              autocomplete="new-password"
              disabled={!draft.existing}
              placeholder={draft.existing ? "" : "Generated for a new repository"}
              bind:value={draft.password}
            />
          </div>
        </div>
      {/if}

      <fieldset class="grid gap-2">
        <legend class="mb-1 text-muted">
          <Help label="Keep snapshots">Each run adds a snapshot. After every run the older ones are thinned out, so the repository does not keep growing.</Help>
        </legend>
        <div class="flex flex-wrap gap-2">
          {#each Object.entries(KEEP_PRESETS) as [name, p] (name)}
            <label class="flex cursor-pointer items-center gap-2 rounded-md border px-3 py-1.5 {preset === name ? 'border-accent bg-accent-soft' : 'border-line'}" title={p.hint}>
              <input type="radio" class="accent-accent" name="{id}-keep" checked={preset === name} onchange={() => (draft.keep = { ...KEEP_PRESETS[name as KeepPreset].keep })} />
              {p.label}
            </label>
          {/each}
          {#if preset === "custom"}
            <span class="self-center text-muted">Custom</span>
          {/if}
        </div>
        {#if preset !== "custom"}<p class="text-xs text-muted">{KEEP_PRESETS[preset].hint}</p>{/if}
        <Disclosure label="Advanced" summary={describeKeep(draft.keep)} bind:open={keepOpen}>
          {#snippet help()}How many daily, weekly, monthly and yearly snapshots stay. With all four at zero, every snapshot is kept.{/snippet}
          <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
            {#each ["daily", "weekly", "monthly", "yearly"] as const as period (period)}
              <div class="grid gap-1">
                <label for="{id}-keep-{period}" class="text-muted capitalize">{period}</label>
                <input id="{id}-keep-{period}" class="input" type="number" min="0" max="9999" bind:value={draft.keep[period]} />
              </div>
            {/each}
          </div>
        </Disclosure>
      </fieldset>
    {/if}

    {#if error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
    {#if problem}<p class="text-muted">{problem}</p>{/if}
  </form>
  {#snippet actions()}
    <button type="submit" form="{id}-form" class="btn btn-primary" disabled={pending || !!problem} title={problem}>
      {destination ? "Save" : "Add destination"}
    </button>
  {/snippet}
</Modal>
