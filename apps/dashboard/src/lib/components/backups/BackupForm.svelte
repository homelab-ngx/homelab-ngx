<script lang="ts">
  import { untrack } from "svelte";
  import { api, call, describe } from "#lib/api/client.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import PathInput from "#lib/components/PathInput.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import {
    draftOf,
    draftProblem,
    inputOf,
    KIND_LABEL,
    newSource,
    allTicked,
    parseSchedule,
    selectableFolders,
    tickFolders,
    toCron,
    WEEKDAYS,
    type Backup,
    type Coverage,
    type Destination,
    type FolderRef,
    type SourceDraft,
  } from "#lib/backups.ts";

  interface Props {
    /** The backup to change; null makes a new one. */
    backup: Backup | null;
    coverage: Coverage | undefined;
    destinations: Destination[];
    /** Recommended folders to tick when the form opens. */
    preselect?: { stack: string; key: string }[];
    onSaved: () => void;
    onClose: () => void;
    onDelete?: (b: Backup) => void;
    /** The operator wants to confirm a restic destination's password. */
    onConfirmPassword: (d: Destination) => void;
    /** What a stack is called on the dashboard; its name by default. */
    stackTitle?: (stack: string) => string;
  }
  let { backup, coverage, destinations, preselect = [], onSaved, onClose, onDelete, onConfirmPassword, stackTitle = (n) => n }: Props =
    $props();

  const id = $props.id();
  /** The editable copy; the form is mounted anew for each backup. */
  let draft = $state(
    untrack(() => {
      const d = draftOf(backup);
      for (const p of preselect) {
        if (!d.sources.some((s) => s.kind === "stack" && s.stack === p.stack && s.folder === p.key))
          d.sources.push(newSource("stack", { stack: p.stack, folder: p.key }));
      }
      return d;
    }),
  );
  let error = $state("");
  let pending = $state(false);

  const problem = $derived(draftProblem(draft));
  const stacks = $derived(coverage?.stacks ?? []);
  const folderOf = (stack: string, key: string) => stacks.find((s) => s.stack === stack)?.folders.find((f) => f.key === key);
  /** Stack folders this backup includes that their stack no longer recommends, or whose stack is gone. */
  const orphans = $derived(draft.sources.filter((s) => s.kind === "stack" && !folderOf(s.stack, s.folder)));
  const custom = $derived(draft.sources.filter((s) => s.kind === "path"));
  const hasCore = $derived(draft.sources.some((s) => s.kind === "core"));
  const has = (stack: string, key: string) => draft.sources.some((s) => s.kind === "stack" && s.stack === stack && s.folder === key);

  function setCore(on: boolean) {
    if (on && !hasCore) draft.sources.unshift(newSource("core"));
    if (!on) draft.sources = draft.sources.filter((s) => s.kind !== "core");
  }
  /** Settings of folders unticked in this dialog, so ticking one again brings them back. */
  const parked = new Map<string, SourceDraft>();
  const setFolders = (folders: FolderRef[], on: boolean) => (draft.sources = tickFolders(draft.sources, folders, on, parked));
  const setFolder = (stack: string, key: string, on: boolean) => setFolders([{ stack, key }], on);
  /** The folders that can be ticked: one that cannot be found on the server stays out of Select all. */
  const selectable = $derived(selectableFolders(stacks));
  const everything = $derived(allTicked(draft.sources, selectable));
  const remove = (s: SourceDraft) => (draft.sources = draft.sources.filter((x) => x !== s));
  function setDestination(destId: number, on: boolean) {
    if (on && !draft.destinations.includes(destId)) draft.destinations.push(destId);
    if (!on) draft.destinations = draft.destinations.filter((d) => d !== destId);
  }

  /** Others that already include a folder, leaving this backup out. */
  const alsoIn = (names: string[]) => names.filter((n) => n !== backup?.name);

  function sourceLabel(s: SourceDraft): string {
    if (s.kind === "core") return "Server settings";
    if (s.kind === "path") return s.path;
    return `${stackTitle(s.stack)}: ${folderOf(s.stack, s.folder)?.label ?? s.folder}`;
  }
  function defaultRemote(s: SourceDraft): string {
    const host = s.kind === "path" ? s.path : s.kind === "stack" ? (folderOf(s.stack, s.folder)?.host_path ?? "") : "";
    return host.replace(/^\/+/, "");
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (problem) return;
    pending = true;
    error = "";
    try {
      const body = inputOf(draft);
      if (backup) await call(api.PATCH("/backups/{id}", { params: { path: { id: backup.id } }, body }));
      else await call(api.POST("/backups", { body }));
      onSaved();
    } catch (err) {
      error = describe(err);
    } finally {
      pending = false;
    }
  }
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title={backup ? `Edit ${backup.name}` : "New backup"} wide>
  <form id="{id}-form" class="grid gap-6" onsubmit={submit}>
    <div class="grid gap-1 sm:max-w-sm">
      <label for="{id}-name" class="text-muted">Name</label>
      <input id="{id}-name" class="input" autocomplete="off" placeholder="Everything important" bind:value={draft.name} />
    </div>

    <fieldset class="grid gap-3">
      <legend class="mb-2 font-semibold">What to back up</legend>

      <div class="flex flex-wrap items-center gap-x-2">
        <input type="checkbox" class="accent-accent" aria-label="Server settings" checked={hasCore} onchange={(e) => setCore(e.currentTarget.checked)} />
        <Help label="Server settings">
          The dashboard's database, the values filled in for the stacks (encrypted) and the list of installed stacks. With these a rebuilt host can bring
          its stacks back. Small.
        </Help>
      </div>

      {#if selectable.length > 1}
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-muted">Recommended folders</span>
          <button type="button" class="btn btn-sm" aria-label="{everything ? 'Deselect' : 'Select'} all recommended folders" onclick={() => setFolders(selectable, !everything)}>
            {everything ? "Deselect all" : "Select all"}
          </button>
        </div>
      {/if}

      {#each stacks as st (st.stack)}
        {@const mine = selectable.filter((f) => f.stack === st.stack)}
        {@const whole = allTicked(draft.sources, mine)}
        <div>
          <div class="flex flex-wrap items-center gap-2">
            <h3 class="font-semibold">{stackTitle(st.stack)}</h3>
            {#if mine.length > 1}
              <button type="button" class="btn btn-sm" aria-label="{whole ? 'Deselect' : 'Select'} all folders of {stackTitle(st.stack)}" onclick={() => setFolders(mine, !whole)}>
                {whole ? "Deselect all" : "Select all"}
              </button>
            {/if}
          </div>
          <ul class="mt-1 grid gap-1.5">
            {#each st.folders as f (f.key)}
              {@const others = alsoIn(f.covered_by)}
              <li>
                <label class="flex items-start gap-2">
                  <input
                    type="checkbox"
                    class="mt-0.5 accent-accent"
                    checked={has(st.stack, f.key)}
                    disabled={!!f.problem && !has(st.stack, f.key)}
                    onchange={(e) => setFolder(st.stack, f.key, e.currentTarget.checked)}
                  />
                  <span class="min-w-0">
                    {f.label}
                    {#if others.length > 0}<span class="text-muted"> · also in {others.join(", ")}</span>{/if}
                    <span class="block text-xs break-all text-muted">{f.host_path || "–"}</span>
                    {#if f.problem}<span class="block text-xs text-warn">{f.problem}</span>{/if}
                  </span>
                </label>
              </li>
            {/each}
          </ul>
        </div>
      {:else}
        <p class="text-muted">No stack recommends folders yet.</p>
      {/each}

      {#if orphans.length > 0}
        <div>
          <h3 class="font-semibold">
            <Help label="No longer recommended">
              The stack was removed or no longer recommends these folders. Each run reports them as failed until they are removed here.
            </Help>
          </h3>
          <ul class="mt-1 grid gap-1.5">
            {#each orphans as s (s.stack + s.folder)}
              <li class="flex flex-wrap items-center gap-3">
                <span class="break-all">{s.stack}: {s.folder}</span>
                <Pill tone="warn">Missing</Pill>
                <button type="button" class="btn btn-sm" onclick={() => remove(s)}>Remove</button>
              </li>
            {/each}
          </ul>
        </div>
      {/if}

      <div>
        <h3 class="font-semibold">
          <Help label="Other folders">Any folder on the host, as its full path. For data that belongs to no stack.</Help>
        </h3>
        <div class="mt-1 grid gap-1.5">
          {#each custom as s, i (i)}
            <div class="flex items-center gap-1.5">
              <PathInput class="flex-1" aria-label="Folder path" placeholder="/var/mnt/pool/documents" bind:value={s.path} />
              <button type="button" class="btn btn-sm" aria-label="Remove {s.path || 'this folder'}" onclick={() => remove(s)}>Remove</button>
            </div>
          {/each}
          <div><button type="button" class="btn btn-sm" onclick={() => draft.sources.push(newSource("path"))}>Add a folder</button></div>
        </div>
      </div>
    </fieldset>

    <fieldset class="grid gap-1.5">
      <legend class="mb-2 font-semibold">
        <Help label="Where to">Several destinations keep copies in several places. A run shows the result for each.</Help>
      </legend>
      {#each destinations as d (d.id)}
        {@const ready = d.kind !== "restic" || d.password_confirmed}
        <div class="flex flex-wrap items-center gap-2">
          <label class="flex items-center gap-2">
            <input type="checkbox" class="accent-accent" checked={draft.destinations.includes(d.id)} onchange={(e) => setDestination(d.id, e.currentTarget.checked)} />
            {d.name}
            <span class="text-muted">{KIND_LABEL[d.kind]}</span>
          </label>
          {#if !ready}
            <Pill tone="warn">Skipped</Pill>
            <button type="button" class="btn btn-sm" onclick={() => onConfirmPassword(d)}>Save password…</button>
          {/if}
        </div>
      {:else}
        <p class="text-muted">No destination yet. Add one under Destinations first.</p>
      {/each}
    </fieldset>

    <fieldset class="grid gap-2">
      <legend class="mb-2 font-semibold">When</legend>
      <label class="flex flex-wrap items-center gap-2">
        <input type="radio" class="accent-accent" name="{id}-when" value="daily" bind:group={draft.schedule.mode} />
        Every day
      </label>
      <label class="flex flex-wrap items-center gap-2">
        <input type="radio" class="accent-accent" name="{id}-when" value="weekly" bind:group={draft.schedule.mode} />
        Every week
      </label>
      <label class="flex flex-wrap items-center gap-2">
        <input type="radio" class="accent-accent" name="{id}-when" value="manual" bind:group={draft.schedule.mode} />
        Manual only
      </label>
      {#if draft.schedule.mode === "custom"}
        <label class="flex flex-wrap items-center gap-2">
          <input type="radio" class="accent-accent" name="{id}-when" value="custom" bind:group={draft.schedule.mode} />
          Custom <span class="text-muted">{draft.schedule.cron} (under Advanced)</span>
        </label>
      {/if}
      {#if draft.schedule.mode === "daily" || draft.schedule.mode === "weekly"}
        <div class="flex flex-wrap items-end gap-3">
          {#if draft.schedule.mode === "weekly"}
            <div class="grid gap-1">
              <label for="{id}-weekday" class="text-muted">Day</label>
              <select id="{id}-weekday" class="input w-auto" bind:value={draft.schedule.weekday}>
                {#each WEEKDAYS as day, i (day)}<option value={i}>{day}</option>{/each}
              </select>
            </div>
          {/if}
          <div class="grid gap-1">
            <label for="{id}-time" class="text-muted">Time</label>
            <input id="{id}-time" class="input w-auto" type="time" required bind:value={draft.schedule.time} />
          </div>
        </div>
      {/if}
      <div class="mt-1 flex flex-wrap items-center gap-x-2">
        <input type="checkbox" class="accent-accent" aria-label="Turned on" bind:checked={draft.enabled} />
        <Help label="Turned on">When off, the backup does not run on its schedule, and nothing warns that it has stopped.</Help>
      </div>
    </fieldset>

    <Disclosure label="Advanced" boxed>
      <div class="mt-1 grid gap-5">
        <div class="grid gap-1 sm:max-w-sm">
          <label for="{id}-cron" class="text-muted">
            <Help label="Schedule as cron">
              Five parts: minute, hour, day of month, month, day of week. 0 */6 * * * runs every six hours. The choice under When rewrites this.
            </Help>
          </label>
          <input
            id="{id}-cron"
            class="input"
            autocomplete="off"
            placeholder="0 4 * * *"
            bind:value={
              () => (draft.schedule.mode === "custom" ? draft.schedule.cron : toCron(draft.schedule)),
              (v) => {
                const p = parseSchedule(v);
                draft.schedule = p.mode === "custom" ? { ...p, cron: v } : p;
              }
            }
          />
        </div>

        {#if draft.sources.some((s) => s.kind !== "path" || s.path.trim())}
          <div class="grid gap-3">
            <h3 class="font-semibold">Per folder</h3>
            {#each draft.sources as s, i (i)}
              {#if s.kind !== "path" || s.path.trim()}
                {@const declared = s.kind === "stack" ? (folderOf(s.stack, s.folder)?.excludes ?? []) : []}
                <div class="grid gap-2 rounded-md border border-line px-3 py-2">
                  <h4 class="font-semibold break-all">{sourceLabel(s)}</h4>
                  <div class="grid gap-1">
                    <label for="{id}-remote-{i}" class="text-muted">
                      <Help label="Folder at the destination (optional)">
                        Where this folder goes at an S3 or rclone destination. Empty keeps the path it has on the host; / is the top of the destination.
                      </Help>
                    </label>
                    <input id="{id}-remote-{i}" class="input" autocomplete="off" placeholder={defaultRemote(s) || "Same as on the host"} bind:value={s.remote} />
                  </div>
                  <div class="flex flex-wrap items-center gap-x-2">
                    <input type="checkbox" class="accent-accent" aria-label="Compare by size only" bind:checked={s.sizeOnly} />
                    <Help label="Compare by size only">
                      Treats a file as unchanged when its size is the same, whatever its date. For a folder first uploaded with another tool. S3 and rclone
                      only.
                    </Help>
                  </div>
                  {#if s.kind !== "core"}
                    <div class="grid gap-1.5">
                      <span class="text-muted">
                        <Help label="Skip patterns">
                          Leaves files out, such as caches. A pattern starts with / (top of the folder) or **/ (anywhere) and may use * and ?. A destination
                          that deletes removed files deletes skipped ones too.
                        </Help>
                      </span>
                      {#if declared.length > 0}
                        <ul class="grid gap-0.5 text-xs">
                          {#each declared as e (e.pattern)}
                            <li>{e.pattern} <span class="text-muted">· from the stack{e.reason ? ` · ${e.reason}` : ""}</span></li>
                          {/each}
                        </ul>
                      {/if}
                      {#each s.excludes as e, j (j)}
                        <div class="flex items-center gap-1.5">
                          <input class="input min-w-0 flex-1" aria-label="Skip pattern" autocomplete="off" placeholder="/cache/**" bind:value={e.pattern} />
                          <input class="input min-w-0 flex-1" aria-label="Why it is skipped" autocomplete="off" placeholder="Reason (optional)" bind:value={e.reason} />
                          <button type="button" class="btn btn-sm" aria-label="Remove the pattern {e.pattern}" onclick={() => s.excludes.splice(j, 1)}>Remove</button>
                        </div>
                      {/each}
                      <div><button type="button" class="btn btn-sm" onclick={() => s.excludes.push({ pattern: "", reason: "" })}>Add a skip pattern</button></div>
                    </div>
                  {/if}
                </div>
              {/if}
            {/each}
          </div>
        {/if}

        <div class="grid gap-3 sm:grid-cols-2">
          <div class="grid content-start gap-1">
            <label for="{id}-bwlimit" class="text-muted">
              <Help label="Upload speed limit (optional)">An amount per second with K, M or G: 4M is four megabytes a second.</Help>
            </label>
            <input id="{id}-bwlimit" class="input" autocomplete="off" placeholder="No limit" bind:value={draft.bwlimit} />
          </div>
          <div class="grid content-start gap-1">
            <label for="{id}-transfers" class="text-muted">
              <Help label="Files sent at once">How many files upload at the same time. 0 lets the tool decide (usually 4). No effect on restic.</Help>
            </label>
            <input id="{id}-transfers" class="input" type="number" min="0" max="64" bind:value={draft.transfers} />
          </div>
        </div>

        <div class="grid gap-1">
          <label for="{id}-hc" class="text-muted">
            <Help label="Healthcheck ping (optional)">
              A ping address from a service such as Healthchecks.io, or its check id. Called when a run starts and ends, so that service can warn when the
              host goes quiet.
            </Help>
          </label>
          <input id="{id}-hc" class="input" autocomplete="off" placeholder="https://hc-ping.com/…" bind:value={draft.healthcheck} />
        </div>
      </div>
    </Disclosure>

    {#if error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
    <!-- A disabled button's title does not show on a phone: say what is missing. -->
    {#if problem}<p class="text-muted">{problem}</p>{/if}
  </form>
  {#snippet actions()}
    {#if backup && onDelete}
      <button type="button" class="btn btn-danger" disabled={backup.running} title={backup.running ? "Wait for the run to end" : ""} onclick={() => onDelete(backup)}>Delete…</button>
    {/if}
    <button type="submit" form="{id}-form" class="btn btn-primary" disabled={pending || !!problem} title={problem}>
      {backup ? "Save" : "Create backup"}
    </button>
  {/snippet}
</Modal>
