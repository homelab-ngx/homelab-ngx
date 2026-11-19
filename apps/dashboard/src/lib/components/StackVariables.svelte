<script lang="ts">
  import Help from "./Help.svelte";
  import { useQueryClient } from "@tanstack/svelte-query";
  import { api, call, type Stack } from "#lib/api/client.ts";
  import { keys } from "#lib/api/queries.ts";
  import { stamp } from "#lib/format.ts";
  import { browseHref } from "#lib/storage.ts";
  import {
    generateKey,
    groupVariables,
    mountText,
    nameProblem,
    ownerText,
    needsValue,
    variableError,
    type StackVariable,
    type VariableKind,
  } from "#lib/variables.ts";
  import { FolderOpen } from "@lucide/svelte";
  import Disclosure from "./Disclosure.svelte";
  import Modal from "./Modal.svelte";
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
  /** From manifest version 7 an unset env variable keeps the value the stack's own config gives. */
  const ownDefaults = $derived((stack.fetched?.manifest.version ?? 0) >= 7);
  const stackValue = (v: StackVariable) => ownDefaults && v.declared && v.kind === "env" && v.source === "unset";
  const declaresAny = $derived(groups.path.length + groups.address.length + groups.env.length + groups.secret.length > 0);

  /** Unsaved input per variable; a stored plain value or path shows until edited. */
  let drafts = $state<Record<string, string>>({});
  /** A request is out: Apply, a row's menu action, or Add. */
  let pending = $state(false);
  /** Refused requests, shown under the row (or the Add form) they came from. */
  let errors = $state<Record<string, string>>({});
  let note = $state("");
  let removing = $state<StackVariable | null>(null);

  let newName = $state("");
  let newValue = $state("");
  let newSecret = $state(false);
  const newProblem = $derived(nameProblem(newName) || (stack.variables.some((v) => v.name === newName) ? `${newName} already exists.` : ""));
  /** The key the Add form's errors are filed under; no variable name has a space. */
  const NEW = "new variable";

  const sections: { kind: VariableKind; title: string }[] = [
    { kind: "path", title: "Paths" },
    { kind: "address", title: "Addresses" },
    { kind: "env", title: "Settings" },
    { kind: "secret", title: "Secrets" },
  ];

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

  /** Apply saves every changed row; a refused one keeps its text and shows why. */
  function apply(e: SubmitEvent) {
    e.preventDefault();
    const rows = changed.map((v) => ({ v, value: drafts[v.name]! }));
    if (!rows.length) return;
    void act(async () => {
      let saved = 0;
      for (const { v, value } of rows) if (await put(v, value)) saved++;
      return saved ? `${saved} saved.` : "";
    });
  }

  function reset() {
    drafts = {};
    errors = {};
    note = "";
  }

  function remove(v: StackVariable) {
    void act(async () => {
      const ok = await send(v.name, v, () => call<Stack>(api.DELETE("/stacks/{name}/variables/{var}", path(v.name))));
      removing = null;
      return ok ? (v.declared ? `${v.name} cleared.` : `${v.name} removed.`) : "";
    });
  }

  function add(e: SubmitEvent) {
    e.preventDefault();
    e.stopPropagation();
    if (!newName || newProblem || !newValue) return;
    const name = newName;
    void act(async () => {
      if (!(await put({ name, kind: newSecret ? "secret" : "env" }, newValue, newSecret || undefined, NEW))) return "";
      newName = "";
      newValue = "";
      newSecret = false;
      return `${name} added.`;
    });
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
    if (v.set || v.source === "stored") return { tone: "ok", word: "set" };
    if (v.source === "default" || stackValue(v)) return { tone: "idle", word: "default" };
    return { tone: "idle", word: "empty" };
  }

  /** The row's rarer actions; a path's Browse is its one button. */
  function menuOf(v: StackVariable): MenuItem[] {
    const items: MenuItem[] = [];
    if (v.secret) items.push({ label: "Generate", onSelect: () => (drafts[v.name] = generateKey()) });
    if (!v.declared && !v.secret && v.value) items.push({ label: "Make secret", onSelect: () => void act(async () => ((await put(v, v.value!, true)) ? `${v.name} is a secret now.` : "")) });
    if (!v.declared) items.push({ label: "Remove…", danger: true, onSelect: () => (removing = v) });
    else if (!v.required && v.set) items.push({ label: "Clear value…", danger: true, onSelect: () => (removing = v) });
    return items;
  }
</script>

{#snippet helpFor(v: StackVariable)}
  {#if v.description}<span class="block">{v.description}</span>{/if}
  {#if v.kind === "path"}<span class="block">Mounts at {mountText(v)}.</span>{/if}
  {#if v.builtin}<span class="block">This host fills it in{v.secret ? ", and issues a new one when the stack's API access changes" : ""}.</span>{/if}
  {#if v.kind === "address"}<span class="block">The stack's network access to or from these addresses follows what is entered here.</span>{/if}
  {#if ownerText(v)}<span class="block">{ownerText(v)}</span>{/if}
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
      {:else}
        <input
          class="input min-w-0"
          type={v.type === "number" ? "number" : "text"}
          aria-label={v.kind === "path" ? `Host path for ${v.name}` : v.kind === "address" ? `Addresses for ${v.name}` : `Value of ${v.name}`}
          aria-invalid={err ? "true" : undefined}
          placeholder={v.default ??
            (v.kind === "path" ? "/var/mnt/pool/…" : v.kind === "address" ? "192.168.1.20, 192.168.1.0/24" : stackValue(v) ? "The stack's own value" : "Value")}
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
        {/if}
        <RowMenu name={v.name} items={menuOf(v)} />
      </span>
    {/if}
    {#if err}<p role="alert" class="order-2 col-span-full text-bad">{err}</p>{/if}
  </li>
{/snippet}

<section class="card" aria-labelledby="{id}-h">
  <h2 id="{id}-h" class="mb-3 font-semibold">
    <Help label="Variables">
      Values this stack reads, stored encrypted on this host. A path is a host folder mounted into the stack. An address opens
      network access to or from the devices named. A service reads each value from /run/secrets/NAME; settings and addresses are
      also in /run/secrets/env.
    </Help>
  </h2>

  <form onsubmit={apply}>
    {#each sections as sec (sec.kind)}
      {#if groups[sec.kind].length}
        <!-- A band from edge to edge of the card, like a table's header row. -->
        <h3 class="label -mx-4 border-y border-line bg-sunken px-4 py-1.5">{sec.title}</h3>
        <ul class="divide-y divide-line">
          {#each groups[sec.kind] as v (v.name)}
            {@render row(v)}
          {/each}
        </ul>
      {/if}
    {/each}
    {#if !declaresAny}<p class="mt-3 text-muted">The stack declares no variables.</p>{/if}

    {#if host.length}
      <Disclosure label="From the host" summary={String(host.length)} remember="variables-host" class="mt-3 border-t border-line pt-3">
        <ul class="divide-y divide-line">
          {#each host as v (v.name)}
            {@render row(v)}
          {/each}
        </ul>
      </Disclosure>
    {/if}

    <Disclosure label="Custom" summary={String(groups.own.length)} remember="variables-custom" class="mt-3 border-t border-line pt-3">
      {#snippet help()}Variables added on this host, and ones the stack no longer declares. They stay until removed.{/snippet}
      <ul class="divide-y divide-line">
        {#each groups.own as v (v.name)}
          {@render row(v)}
        {/each}
      </ul>
      <div class="flex flex-wrap items-center gap-2 py-2">
        <input
          class="input w-auto min-w-0 flex-1 basis-40"
          aria-label="New variable name"
          aria-invalid={newProblem ? "true" : undefined}
          placeholder="NAME"
          autocomplete="off"
          spellcheck="false"
          maxlength="64"
          form="{id}-add"
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
          form="{id}-add"
          bind:value={newValue}
        />
        <label class="flex items-center gap-1.5">
          <input type="checkbox" form="{id}-add" bind:checked={newSecret} />
          Secret
        </label>
        <button type="submit" form="{id}-add" class="btn btn-sm" disabled={pending || !newName || !!newProblem || !newValue}>Add</button>
      </div>
      {#if newProblem}<p class="text-warn">{newProblem}</p>{/if}
      {#if errors[NEW]}<p role="alert" class="text-bad">{errors[NEW]}</p>{/if}
    </Disclosure>

    <div class="mt-3 flex flex-wrap items-center justify-end gap-2 border-t border-line pt-3">
      {#if note}<p role="status" class="mr-auto text-muted">{note}</p>{/if}
      <button type="button" class="btn" disabled={pending || (changed.length === 0 && Object.keys(drafts).length === 0)} onclick={reset}>Reset</button>
      <button type="submit" class="btn btn-primary" disabled={pending || changed.length === 0}>{pending ? "Applying…" : "Apply"}</button>
    </div>
  </form>
  <!-- The Add row's own form: its fields sit inside the fold above and point here. -->
  <form id="{id}-add" onsubmit={add}></form>
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
