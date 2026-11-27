<script lang="ts">
  import Help from "./Help.svelte";
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, type Schema, type Stack } from "#lib/api/client.ts";
  import { keys } from "#lib/api/queries.ts";
  import { stamp } from "#lib/format.ts";
  import { browseHref, trimSlash } from "#lib/storage.ts";
  import {
    ADDRESS_WORDS,
    addressParts,
    addressPreview,
    addressValue,
    declaredReadOnly,
    generateKey,
    groupVariables,
    mountText,
    nameProblem,
    ownerText,
    ownedText,
    needsValue,
    variableError,
    type StackVariable,
  } from "#lib/variables.ts";
  import { FolderOpen, Trash2 } from "@lucide/svelte";
  import Disclosure from "./Disclosure.svelte";
  import Modal from "./Modal.svelte";
  import PathInput from "./PathInput.svelte";
  import Pill, { type Tone } from "./Pill.svelte";
  import RowMenu, { type MenuItem } from "./RowMenu.svelte";

  interface Props {
    stack: Stack;
  }
  let { stack }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  /** Values this host fills in have their own fold; the rest group by kind. */
  const groups = $derived(groupVariables(stack.variables.filter((v) => !v.builtin)));
  const host = $derived(stack.variables.filter((v) => v.builtin));
  /** Env mappings: values the stack reads; settings, addresses and secrets it declares, then the operator's own. */
  const envRows = $derived([...groups.env, ...groups.address, ...groups.secret, ...groups.own.filter((v) => v.kind !== "path")]);
  /** Path mappings: host folders bound into the stack, the declared ones first, then the added folders. */
  const pathRows = $derived([...groups.path, ...groups.own.filter((v) => v.kind === "path")]);
  const folders = $derived(stack.folders ?? []);
  /** The manifest's hint for added folders (version 9): each is named and mounted at <under>/<name>. */
  const hint = $derived((stack.approved ?? stack.fetched)?.manifest.added_folders);
  /** An added folder's name under the hint's directory, or "" for one mounted elsewhere. */
  const folderName = (f: { mount: string }) => {
    const rest = hint && f.mount.startsWith(`${hint.under}/`) ? f.mount.slice(hint.under.length + 1) : "";
    return rest.includes("/") ? "" : rest;
  };
  const named = $derived(folders.filter((f) => folderName(f)));
  const unnamed = $derived(folders.filter((f) => !folderName(f)));
  /** How an added folder is called: its name, or its mount. */
  const folderLabel = (f: { mount: string }) => folderName(f) || f.mount;
  /** From manifest version 7 an unset env variable keeps the value the stack's own config gives. */
  const ownDefaults = $derived((stack.fetched?.manifest.version ?? 0) >= 7);
  const stackValue = (v: StackVariable) => ownDefaults && v.declared && v.kind === "env" && v.source === "unset";

  /** Unsaved input per variable; a stored plain value or path shows until edited. */
  let drafts = $state<Record<string, string>>({});
  /** An address row's other addresses as typed, so a separator being typed is kept; its draft holds the parsed value. */
  let otherText = $state<Record<string, string>>({});
  /** An address row's Custom box as the operator left it; unset, it is ticked while the value has custom addresses. */
  let custom = $state<Record<string, boolean>>({});
  /** A request is out: Apply or a row's menu action. */
  let pending = $state(false);
  /** Refused requests, shown under the row (or the new mapping) they came from. */
  let errors = $state<Record<string, string>>({});
  let note = $state("");
  let removing = $state<StackVariable | null>(null);

  // A path's folder can be made here: on a pool it is a subvolume, and it gets
  // the owner the stack needs. The places are the mounted pools and partitions.
  let creating = $state<StackVariable | null>(null);
  let place = $state("");
  let folder = $state("");
  const storage = createQuery(() => ({
    queryKey: ["daemon", "storage"] as const,
    queryFn: () => call<Schema<"StorageStatus">>(api.GET("/storage")),
    enabled: creating !== null,
  }));
  const places = $derived([
    ...(storage.data?.pools ?? []).filter((p) => p.state === "mounted").map((p) => p.mountdir),
    ...(storage.data?.partitions ?? []).filter((p) => p.state === "mounted" && !p.read_only).map((p) => p.mountdir),
  ]);
  const chosenPlace = $derived(places.includes(place) ? place : (places[0] ?? ""));
  function askCreate(v: StackVariable) {
    creating = v;
    place = "";
    folder = stack.name;
    delete errors[v.name];
  }
  function create(e: SubmitEvent) {
    e.preventDefault();
    const v = creating;
    if (!v || !chosenPlace || !folder.trim()) return;
    void act(async () => {
      const made = await send(v.name, v, () =>
        call<Stack>(api.POST("/stacks/{name}/variables/{var}/folder", { ...path(v.name), body: { place: chosenPlace, folder: folder.trim() } })),
      );
      if (made) creating = null;
      return made ? `${chosenPlace}/${folder.trim()} made.` : "";
    });
  }

  // Changing a folder's owner to the stack ids cannot be undone: its path is typed.
  let owning = $state<StackVariable | null>(null);
  let typed = $state("");
  const ids = (uid?: number, gid?: number) => `${uid ?? "?"}:${gid ?? "?"}`;
  function own(e: SubmitEvent) {
    e.preventDefault();
    const v = owning;
    if (!v || typed !== v.value) return;
    void act(async () => {
      const done = await send(v.name, v, () => call<Stack>(api.POST("/stacks/{name}/variables/{var}/own", { ...path(v.name), body: { confirm: typed } })));
      if (done) owning = null;
      return done ? `${v.value} has the stack ids.` : "";
    });
  }

  // The new mapping: one row for either kind, shown on demand and saved with Apply.
  let adding = $state(false);
  let newType = $state<"env" | "path">("env");
  let newName = $state("");
  let newValue = $state("");
  let newSecret = $state(false);
  let newPath = $state("");
  let newMount = $state("");
  let newReadOnly = $state(true);
  /** The mount (or, with the hint, the name) last filled in from the host folder; one the operator typed is kept. */
  let filledMount = "";
  /** With the hint, the Path form asks for a name: one folder name, no '/', '\\' or ':'. */
  const nameBad = $derived(!!hint && newType === "path" && /^\.{1,2}$|[\/\\:]/.test(newMount.trim()));
  const newProblem = $derived(nameProblem(newName) || (stack.variables.some((v) => v.name === newName) ? `${newName} already exists.` : ""));
  const canAdd = $derived(newType === "env" ? !!newName && !newProblem && !!newValue : !!trimSlash(newPath.trim()) && !!newMount.trim() && !nameBad);
  /** The key the new mapping's errors are filed under; no variable name has a space. */
  const NEW = "new mapping";

  /** /mnt/<folder name> for a host folder (with the hint, the folder name alone), or "" for none. */
  const mountFor = (p: string) => {
    const name = trimSlash(p.trim()).split("/").pop();
    return !name ? "" : hint ? name : `/mnt/${name}`;
  };
  /** Follow the host folder in the mount field until the operator types a mount of their own. */
  function fillMount(p: string) {
    if (newMount !== filledMount) return;
    newMount = filledMount = mountFor(p);
  }

  function closeAdd() {
    adding = false;
    newName = newValue = newPath = newMount = filledMount = "";
    newSecret = false;
    newReadOnly = hint?.read_only ?? true;
    delete errors[NEW];
  }

  const unreadable = $derived(Boolean(stack.variables_error));

  function openAdd() {
    if (unreadable) return;
    adding = true;
    newReadOnly = hint?.read_only ?? true;
  }

  const isChanged = (v: StackVariable) => {
    const d = drafts[v.name];
    return d !== undefined && d !== "" && d !== v.value;
  };
  const changed = $derived(stack.variables.filter(isChanged));

  /** One request for one variable; a refusal is filed under key. */
  async function send(key: string, v: Pick<StackVariable, "name" | "kind">, request: () => Promise<Stack>): Promise<boolean> {
    delete errors[key];
    try {
      const s = await request();
      client.setQueryData([...keys.stacks, stack.name], s);
      delete drafts[v.name];
      delete otherText[v.name];
      delete custom[v.name];
      return true;
    } catch (e) {
      errors[key] = variableError(e, v);
      return false;
    }
  }

  const path = (name: string) => ({ params: { path: { name: stack.name, var: name } } });
  const put = (v: Pick<StackVariable, "name" | "kind">, value: string, secret?: boolean, key = v.name) =>
    send(key, v, () => call<Stack>(api.PUT("/stacks/{name}/variables/{var}", { ...path(v.name), body: secret === undefined ? { value } : { value, secret } })));

  /** Run one or more requests as a single action, then say what happened. */
  async function act(work: () => Promise<string>) {
    pending = true;
    note = "";
    try {
      note = await work();
    } finally {
      pending = false;
      void client.invalidateQueries({ queryKey: keys.stacks });
    }
  }

  /** Apply saves every changed row and the new mapping; a refused one keeps its text and shows why. */
  function apply(e: SubmitEvent) {
    e.preventDefault();
    if (unreadable) return;
    const rows = changed.map((v) => ({ v, value: drafts[v.name]! }));
    const added = adding && canAdd ? newMapping() : null;
    if (!rows.length && !added) return;
    void act(async () => {
      let saved = 0;
      for (const { v, value } of rows) if (await put(v, value)) saved++;
      const said = added ? await added() : "";
      return [saved ? `${saved} saved.` : "", said].filter(Boolean).join(" ");
    });
  }

  function reset() {
    drafts = {};
    otherText = {};
    custom = {};
    errors = {};
    note = "";
    closeAdd();
  }

  function remove(v: StackVariable) {
    void act(async () => {
      const ok = await send(v.name, v, () => call<Stack>(api.DELETE("/stacks/{name}/variables/{var}", path(v.name))));
      removing = null;
      return ok ? (v.declared ? `${v.name} cleared.` : `${v.name} removed.`) : "";
    });
  }

  // Added folders: every change sends the whole list, and the daemon checks each folder and mount.
  type Folder = Schema<"StackFolder">;
  const asIn = (f: Folder): Schema<"StackFolderIn"> => ({ path: f.path, mount: f.mount, read_only: f.read_only });
  const saveFolders = (key: string, list: Schema<"StackFolderIn">[]) =>
    send(key, { name: key, kind: "path" }, () => call<Stack>(api.PUT("/stacks/{name}/folders", { params: { path: { name: stack.name } }, body: { folders: list } })));

  /** The new mapping's request, with its fields as they are now; it says what it did, or "" when refused. */
  function newMapping(): () => Promise<string> {
    if (newType === "env") {
      const [name, value, secret] = [newName, newValue, newSecret];
      return async () => {
        if (!(await put({ name, kind: secret ? "secret" : "env" }, value, secret || undefined, NEW))) return "";
        closeAdd();
        return `${name} added.`;
      };
    }
    const path = trimSlash(newPath.trim());
    const name = newMount.trim();
    const mount = hint ? `${hint.under}/${name}` : name;
    const read_only = newReadOnly;
    return async () => {
      if (!(await saveFolders(NEW, [...folders.map(asIn), { path, mount, read_only }]))) return "";
      closeAdd();
      return `${path} added ${hint ? `as ${name}` : `at ${mount}`}. It is bound from the next restart.`;
    };
  }

  function folderMenu(f: Folder): MenuItem[] {
    const key = `folder ${f.mount}`;
    const label = folderLabel(f);
    return [
      {
        label: f.read_only ? "Allow writes" : "Make read-only",
        onSelect: () =>
          void act(async () =>
            (await saveFolders(
              key,
              folders.map((o) => (o.mount === f.mount ? { ...asIn(o), read_only: !f.read_only } : asIn(o))),
            ))
              ? `${label} ${f.read_only ? "writable" : "read-only"} from the next restart.`
              : "",
          ),
      },
      {
        label: "Remove",
        danger: true,
        onSelect: () =>
          void act(async () => ((await saveFolders(key, folders.filter((o) => o.mount !== f.mount).map(asIn))) ? `${label} removed. The folder stays.` : "")),
      },
    ];
  }

  /** What removing does, for the confirmation. */
  function removeText(v: StackVariable | null): string {
    if (!v) return "";
    if (!v.declared) return "Deletes the value. A service reading it fails or falls back at its next start.";
    if (v.default !== undefined) return `Deletes the stored value. The default ${v.default} applies again.`;
    if (ownDefaults && v.kind === "env") return "Deletes the stored value. The stack's own value applies from its next restart.";
    if (v.kind === "path") return "Deletes the stored host path. The stack runs without this mount from its next restart.";
    if (v.kind === "address") return "Deletes the addresses. Network access that uses them closes at the next restart.";
    return "Deletes the stored value. The stack goes without it.";
  }

  const fileOnly = (v: StackVariable) => v.kind !== "path" && v.set && !v.in_env;
  const hasAbout = (v: StackVariable) => !!(v.description || v.updated_at || v.builtin || v.kind === "path" || v.kind === "address" || fileOnly(v));

  /** A row's state: one mark and one word. */
  function stateOf(v: StackVariable): { tone: Tone; word: string } {
    if (v.builtin) return { tone: "info", word: "host" };
    if (needsValue(v)) return { tone: "warn", word: "required" };
    if (v.owner_state === "root") return { tone: "warn", word: "root's" };
    if (v.owner_state === "group_differs") return { tone: "warn", word: "own group" };
    if (v.set || v.source === "stored") return { tone: "ok", word: "set" };
    if (v.source === "default" || stackValue(v)) return { tone: "idle", word: "default" };
    return { tone: "idle", word: "empty" };
  }

  /** Bind a declared path read-only although the stack asks to write, or as it asks again. */
  function setReadOnly(v: StackVariable, ro: boolean) {
    void act(async () => {
      const ok = await send(v.name, v, () =>
        call<Stack>(api.PUT("/stacks/{name}/variables/{var}", { ...path(v.name), body: { value: v.value!, read_only: ro } })),
      );
      return ok ? `${v.name} ${ro ? "read-only" : "writable"} from the next restart.` : "";
    });
  }

  /** The row's rarer actions; a path's Browse is its one button. */
  function menuOf(v: StackVariable): MenuItem[] {
    const items: MenuItem[] = [];
    if (v.secret) items.push({ label: "Generate", onSelect: () => (drafts[v.name] = generateKey()) });
    if (!v.declared && !v.secret && v.value) items.push({ label: "Make secret", onSelect: () => void act(async () => ((await put(v, v.value!, true)) ? `${v.name} is a secret now.` : "")) });
    if (!v.declared) items.push({ label: "Remove…", danger: true, onSelect: () => (removing = v) });
    else if (!v.required && v.set) items.push({ label: "Clear value…", danger: true, onSelect: () => (removing = v) });
    if (v.kind === "path" && v.declared && v.set && v.value && !declaredReadOnly(v)) {
      const ro = !v.read_only_by_operator;
      items.push({ label: ro ? "Make read-only" : "Allow writes", onSelect: () => setReadOnly(v, ro) });
    }
    if (v.kind === "path" && v.value && v.owner_state && v.owner_state !== "ok")
      items.push({ label: "Change owner…", danger: true, onSelect: () => ((owning = v), (typed = ""), delete errors[v.name]) });
    return items;
  }
</script>

{#snippet helpFor(v: StackVariable)}
  {#if v.description}<span class="block">{v.description}</span>{/if}
  {#if v.kind === "path"}<span class="block">Mounts at {mountText(v)}.</span>{/if}
  {#if v.builtin}<span class="block">This host fills it in{v.secret ? ", and issues a new one when the stack's API access changes" : ""}.</span>{/if}
  {#if v.kind === "address" && v.single}
    <span class="block">One IPv4 address. The stack gets it, and its network access to or from it follows.</span>
  {:else if v.kind === "address"}
    <span class="block">The ticked networks and the custom addresses add up: the stack gets all of them, and its network access to or from them follows. A network that is off adds nothing.</span>
    <span class="block">The line under the boxes is what the stack gets.</span>
  {/if}
  {#if ownerText(v)}<span class="block">{ownerText(v)}</span>{/if}
  {#if ownedText(v)}<span class="block">{ownedText(v)}</span>{/if}
  {#if fileOnly(v)}<span class="block">Delivered only as /run/secrets/{v.name}, not in /run/secrets/env.</span>{/if}
  {#if v.secret && !v.builtin}<span class="block">Generate, in the row's menu, fills in a random 32-byte key.</span>{/if}
  {#if v.updated_at}<span class="block">Set {stamp(v.updated_at)}</span>{/if}
{/snippet}

{#snippet row(v: StackVariable)}
  {@const draft = drafts[v.name]}
  {@const st = stateOf(v)}
  {@const err = errors[v.name] ?? ""}
  <!-- One line from lg up: name, state, field, actions. On a phone the name and state share a line above the field. -->
  <li class="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1.5 py-2 lg:grid-cols-[minmax(0,16rem)_6rem_minmax(0,1fr)_4rem]">
    {#if hasAbout(v)}
      <Help label={v.name} class="break-all">{@render helpFor(v)}</Help>
    {:else}
      <span class="break-all">{v.name}</span>
    {/if}
    <Pill tone={st.tone}>{st.word}</Pill>
    {#if v.builtin}
      <span class="break-all">{v.secret ? (v.set ? "issued" : "not issued") : v.value}</span>
      <span></span>
    {:else}
      {#if v.secret}
        <input
          class="input min-w-0"
          type={draft ? "text" : "password"}
          aria-label="New value for {v.name}"
          aria-invalid={err ? "true" : undefined}
          placeholder={v.set ? "Replace the value" : "Value"}
          autocomplete="new-password"
          spellcheck="false"
          value={draft ?? ""}
          oninput={(e) => (drafts[v.name] = e.currentTarget.value)}
        />
      {:else if v.enum?.length || v.type === "boolean"}
        {@const choices = v.enum?.length ? v.enum : ["true", "false"]}
        <select
          class="input min-w-0"
          aria-label="Value of {v.name}"
          aria-invalid={err ? "true" : undefined}
          value={draft ?? v.value ?? ""}
          onchange={(e) => (drafts[v.name] = e.currentTarget.value)}
        >
          <option value="" disabled>{stackValue(v) ? "The stack's own value" : "Choose…"}</option>
          {#each choices as c (c)}<option value={c}>{c}</option>{/each}
        </select>
      {:else if v.kind === "address" && v.single}
        <input
          class="input min-w-0"
          aria-label="Address for {v.name}"
          aria-invalid={err ? "true" : undefined}
          placeholder="192.168.1.20"
          autocomplete="off"
          spellcheck="false"
          value={draft ?? v.value ?? ""}
          oninput={(e) => (drafts[v.name] = e.currentTarget.value)}
        />
      {:else if v.kind === "address"}
        {@const parts = addressParts(draft ?? v.value ?? "")}
        {@const text = otherText[v.name] ?? parts.other}
        {@const customOn = custom[v.name] ?? parts.other !== ""}
        {@const gets = v.networks ? addressPreview(draft ?? v.value ?? "", v.networks) : ""}
        <span class="flex min-w-0 flex-col gap-1">
          <span class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1.5" role="group" aria-label="Addresses for {v.name}">
            {#each ADDRESS_WORDS as w (w.word)}
              <label class="flex items-center gap-1.5">
                <input
                  type="checkbox"
                  checked={parts.words.includes(w.word)}
                  onchange={(e) =>
                    (drafts[v.name] = addressValue(
                      e.currentTarget.checked ? [...parts.words, w.word] : parts.words.filter((x) => x !== w.word),
                      customOn ? text : "",
                    ))}
                />
                {w.label}
              </label>
            {/each}
            <label class="flex items-center gap-1.5">
              <input
                type="checkbox"
                checked={customOn}
                onchange={(e) => {
                  const on = e.currentTarget.checked;
                  // Unticked, the addresses leave the value but stay typed, for ticking it again.
                  otherText[v.name] = text;
                  custom[v.name] = on;
                  drafts[v.name] = addressValue(parts.words, on ? text : "");
                }}
              />
              Custom
            </label>
            {#if customOn}
              <input
                class="input w-auto min-w-0 flex-1 basis-40"
                aria-label="Custom addresses for {v.name}"
                aria-invalid={err ? "true" : undefined}
                placeholder="192.168.1.20, 10.0.0.0/24"
                autocomplete="off"
                spellcheck="false"
                value={text}
                oninput={(e) => {
                  otherText[v.name] = e.currentTarget.value;
                  drafts[v.name] = addressValue(parts.words, e.currentTarget.value);
                }}
              />
            {/if}
          </span>
          {#if gets}<span class="break-all text-muted">Gets {gets}</span>{/if}
        </span>
      {:else if v.kind === "path"}
        <PathInput
          aria-label="Host path for {v.name}"
          aria-invalid={err ? "true" : undefined}
          placeholder={v.default ?? "/var/mnt/pool/…"}
          bind:value={() => draft ?? v.value ?? "", (x) => (drafts[v.name] = x)}
        />
      {:else}
        <input
          class="input min-w-0"
          type={v.type === "number" ? "number" : "text"}
          aria-label="Value of {v.name}"
          aria-invalid={err ? "true" : undefined}
          placeholder={v.default ?? (stackValue(v) ? "The stack's own value" : "Value")}
          autocomplete="off"
          spellcheck="false"
          value={draft ?? v.value ?? ""}
          oninput={(e) => (drafts[v.name] = e.currentTarget.value)}
        />
      {/if}
      <span class="flex justify-end gap-1">
        {#if v.kind === "path" && v.value}
          <a class="btn btn-sm px-1.5" href={browseHref(v.value)} aria-label="Browse {v.value}" title="Browse files">
            <FolderOpen class="size-4" aria-hidden="true" />
          </a>
        {:else if v.kind === "path" && v.declared}
          <button type="button" class="btn btn-sm whitespace-nowrap" disabled={pending} onclick={() => askCreate(v)}>Create…</button>
        {/if}
        <RowMenu name={v.name} items={menuOf(v)} />
      </span>
    {/if}
    {#if err}<p role="alert" class="order-2 col-span-full text-bad">{err}</p>{/if}
  </li>
{/snippet}

{#snippet folderRow(f: Schema<"StackFolder">)}
  {@const err = errors[`folder ${f.mount}`] ?? ""}
  <li class="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1.5 py-2 lg:grid-cols-[minmax(0,16rem)_6rem_minmax(0,1fr)_4rem]">
    <Help label={folderLabel(f)} class="break-all">
      {#if folderName(f)}
        <span class="block">Mounted at {f.mount} in the stack.</span>
      {:else}
        <span class="block">An added folder: bound into the stack on this host, with no change to its repo.</span>
      {/if}
      {#if f.shadowed_by}<span class="block">Not bound: {f.shadowed_by} in the manifest claims this mount.</span>{/if}
      {#if f.updated_at}<span class="block">Set {stamp(f.updated_at)}</span>{/if}
    </Help>
    {#if f.shadowed_by}
      <Pill tone="warn">not bound</Pill>
    {:else}
      <Pill tone="ok">{f.read_only ? "read-only" : "writable"}</Pill>
    {/if}
    <span class="break-all">{f.path}</span>
    <span class="flex justify-end gap-1">
      <a class="btn btn-sm px-1.5" href={browseHref(f.path)} aria-label="Browse {f.path}" title="Browse files">
        <FolderOpen class="size-4" aria-hidden="true" />
      </a>
      <RowMenu name={folderLabel(f)} items={folderMenu(f)} />
    </span>
    {#if err}<p role="alert" class="order-2 col-span-full text-bad">{err}</p>{/if}
  </li>
{/snippet}

<section class="card" aria-labelledby="{id}-h">
  <h2 id="{id}-h" class="mb-3 font-semibold">
    <Help label="Mappings">
      What this stack reads from this host. An env mapping is a value, stored encrypted; a service reads it from
      /run/secrets/NAME, and settings and addresses are also in /run/secrets/env. A path mapping is a host folder mounted into
      the stack. Folder changes apply at the next restart.
    </Help>
  </h2>

  <form onsubmit={apply}>
    <!-- Bands from edge to edge of the card, like a table's header row. -->
    <h3 class="label -mx-4 border-y border-line bg-sunken px-4 py-1.5">Env mappings</h3>
    {#if envRows.length}
      <ul class="divide-y divide-line">
        {#each envRows as v (v.name)}
          {@render row(v)}
        {/each}
      </ul>
    {:else}
      <p class="py-2 text-muted">None.</p>
    {/if}
    {#if host.length}
      <Disclosure label="From the host" summary={String(host.length)} remember="variables-host" class="border-t border-line py-2">
        <ul class="divide-y divide-line">
          {#each host as v (v.name)}
            {@render row(v)}
          {/each}
        </ul>
      </Disclosure>
    {/if}

    <h3 class="label -mx-4 border-y border-line bg-sunken px-4 py-1.5">Path mappings</h3>
    {#if pathRows.length || unnamed.length}
      <ul class="divide-y divide-line">
        {#each pathRows as v (v.name)}
          {@render row(v)}
        {/each}
        {#each unnamed as f (f.mount)}
          {@render folderRow(f)}
        {/each}
      </ul>
    {:else if !hint}
      <p class="py-2 text-muted">None.</p>
    {/if}
    {#if hint}
      <h4 class="label border-t border-line pt-2">
        <Help label={hint.label}>
          {#if hint.description}<span class="block">{hint.description}</span>{/if}
          <span class="block">Each is a host folder with a name, mounted at {hint.under}/name. Add one with Add mapping, as a path.</span>
        </Help>
      </h4>
      {#if named.length}
        <ul class="divide-y divide-line">
          {#each named as f (f.mount)}
            {@render folderRow(f)}
          {/each}
        </ul>
      {:else}
        <p class="py-2 text-muted">None.</p>
      {/if}
    {/if}

    {#if adding}
      <div id="{id}-new" class="flex flex-wrap items-center gap-2 border-t border-line py-3">
        <select class="input w-auto" aria-label="Mapping type" bind:value={newType}>
          <option value="env">Env</option>
          <option value="path">Path</option>
        </select>
        {#if newType === "env"}
          <input
            class="input w-auto min-w-0 flex-1 basis-40"
            aria-label="New variable name"
            aria-invalid={newProblem ? "true" : undefined}
            placeholder="NAME"
            autocomplete="off"
            spellcheck="false"
            maxlength="64"
            value={newName}
            oninput={(e) => (newName = e.currentTarget.value.toUpperCase())}
          />
          <input
            class="input w-auto min-w-0 flex-[2] basis-56"
            type={newSecret ? "password" : "text"}
            aria-label="New variable value"
            placeholder="Value"
            autocomplete={newSecret ? "new-password" : "off"}
            spellcheck="false"
            bind:value={newValue}
          />
          <label class="flex w-24 items-center gap-1.5">
            <input type="checkbox" bind:checked={newSecret} />
            Secret
          </label>
        {:else}
          <PathInput
            class="flex-[2] basis-56"
            aria-label="Host folder"
            placeholder="/var/mnt/pool/…"
            bind:value={newPath}
            oninput={(e) => fillMount(e.currentTarget.value)}
            onpick={fillMount}
          />
          <input
            class="input w-auto min-w-0 flex-1 basis-40"
            aria-label={hint ? "Name" : "Mount in the stack"}
            aria-invalid={nameBad ? "true" : undefined}
            placeholder={hint ? "Name" : "/mnt/library"}
            autocomplete="off"
            spellcheck="false"
            maxlength={hint ? 64 : undefined}
            bind:value={newMount}
          />
          <!-- RO on the left, RW on the right; the pressed one is how the folder is bound. -->
          <span class="flex" role="group" aria-label="Access">
            {#each [{ ro: true, word: "RO", title: "Read-only" }, { ro: false, word: "RW", title: "Read and write" }] as a, i (a.word)}
              <button
                type="button"
                class="btn aria-pressed:z-10 aria-pressed:border-accent aria-pressed:bg-accent-soft aria-pressed:text-accent {i ? '-ml-px rounded-l-none' : 'rounded-r-none'}"
                aria-pressed={newReadOnly === a.ro}
                title={a.title}
                onclick={() => (newReadOnly = a.ro)}
              >
                {a.word}
              </button>
            {/each}
          </span>
        {/if}
        <button type="button" class="btn btn-sm ml-auto px-1.5" aria-label="Discard the new mapping" title="Discard" onclick={closeAdd}>
          <Trash2 class="size-4" aria-hidden="true" />
        </button>
      </div>
      {#if newType === "env" && newProblem}<p class="text-warn">{newProblem}</p>{/if}
      {#if nameBad}<p class="text-warn">A name has no '/', '\' or ':'.</p>{/if}
      {#if errors[NEW]}<p role="alert" class="text-bad">{errors[NEW]}</p>{/if}
    {/if}

    <div class="flex flex-wrap items-center justify-end gap-2 border-t border-line pt-3">
      <!-- It stays in place and folds the form away again, like a fold's label. -->
      <button type="button" class="btn mr-auto" disabled={unreadable} aria-expanded={adding} aria-controls="{id}-new" onclick={() => (adding ? closeAdd() : openAdd())}>
        Add mapping
      </button>
      {#if note}<p role="status" class="text-muted">{note}</p>{/if}
      <button type="button" class="btn" disabled={pending || (changed.length === 0 && Object.keys(drafts).length === 0 && !adding)} onclick={reset}>Reset</button>
      <button type="submit" class="btn btn-primary" disabled={unreadable || pending || (changed.length === 0 && !(adding && canAdd))}>{pending ? "Applying…" : "Apply"}</button>
    </div>
  </form>
</section>

<Modal
  open={removing !== null}
  onOpenChange={(o) => {
    if (!o) removing = null;
  }}
  title="{removing?.declared ? 'Clear' : 'Remove'} {removing?.name}?"
  description={removeText(removing)}
>
  {#if removing?.secret}<p class="text-muted">A secret's value cannot be recovered.</p>{/if}
  {#if removing && errors[removing.name]}<p role="alert" class="text-bad">{errors[removing.name]}</p>{/if}
  {#snippet actions()}
    <button type="button" class="btn btn-danger" disabled={pending} onclick={() => removing && remove(removing)}>{removing?.declared ? "Clear" : "Remove"}</button>
  {/snippet}
</Modal>

<Modal
  open={creating !== null}
  onOpenChange={(o) => {
    if (!o) creating = null;
  }}
  title="New folder for {creating?.name}"
  description={creating?.owner || creating?.group ? "It is made with the owner the stack needs." : undefined}
>
  <form id="{id}-create" class="grid gap-3" onsubmit={create}>
    <label class="grid gap-1">
      <span class="text-muted">On</span>
      <select class="input" bind:value={() => chosenPlace, (v) => (place = v)} disabled={places.length < 2}>
        {#each places as p (p)}<option value={p}>{p}</option>{/each}
      </select>
    </label>
    <label class="grid gap-1">
      <span class="text-muted">Name</span>
      <input class="input" autocomplete="off" spellcheck="false" pattern="[A-Za-z0-9][A-Za-z0-9._\-]*" maxlength="64" bind:value={folder} />
    </label>
    {#if storage.isSuccess && places.length === 0}
      <p class="text-muted">No pool or partition is mounted. <a class="link" href="/storage">Mount one</a> first.</p>
    {/if}
    {#if creating && errors[creating.name]}<p role="alert" class="text-bad">{errors[creating.name]}</p>{/if}
  </form>
  {#snippet actions()}
    <button type="submit" form="{id}-create" class="btn btn-primary" disabled={pending || !chosenPlace || !folder.trim()}>Create</button>
  {/snippet}
</Modal>

<Modal
  open={owning !== null}
  onOpenChange={(o) => {
    if (!o) owning = null;
  }}
  title="Change the owner of {owning?.value}?"
  description="Everything in it gets this host's ids for the stack. This cannot be undone here."
>
  <form id="{id}-own" class="grid gap-1" onsubmit={own}>
    <p class="text-muted">Owner now: {ids(owning?.folder_uid, owning?.folder_gid)}</p>
    {#if stack.running}<p class="text-warn">Stop the stack first.</p>{/if}
    <label for="{id}-typed" class="text-muted">Type {owning?.value}</label>
    <input id="{id}-typed" class="input" autocomplete="off" spellcheck="false" bind:value={typed} />
    {#if owning && errors[owning.name]}<p role="alert" class="text-bad">{errors[owning.name]}</p>{/if}
  </form>
  {#snippet actions()}
    <button type="submit" form="{id}-own" class="btn btn-danger" disabled={pending || stack.running || typed !== owning?.value}>Change owner</button>
  {/snippet}
</Modal>
