<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { api, call, describe, ProblemError, type Draft, type Stack } from "#lib/api/client.ts";
  import { busy, busyPoll, fetchStack, fetchUpdates, keys } from "#lib/api/queries.ts";
  import Help from "#lib/components/Help.svelte";
  import OperationCard from "#lib/components/OperationCard.svelte";
  import PathInput from "#lib/components/PathInput.svelte";
  import StackMappings from "#lib/components/StackMappings.svelte";
  import { describeSource, draftOf, folderSource, gitSource, grants, stackTitle, validStackName } from "#lib/stacks.ts";
  import { missingVariables } from "#lib/variables.ts";

  type Kind = "git" | "folder" | "write";
  const STEPS = ["Source", "Configure and approve", "Install"] as const;
  const STARTERS = [
    { name: "immich", label: "Immich", url: "https://github.com/homelab-ngx/stack-immich" },
    { name: "navidrome", label: "Navidrome", url: "https://github.com/homelab-ngx/stack-navidrome" },
    { name: "plex-jellyfin", label: "Plex & Jellyfin", url: "https://github.com/homelab-ngx/stack-plex-jellyfin" },
    { name: "samba", label: "Samba", url: "https://github.com/homelab-ngx/stack-samba" },
    { name: "pocket-id", label: "Pocket ID", url: "https://github.com/homelab-ngx/stack-pocket-id" },
  ] as const;
  const BROWSE_STACKS = "https://github.com/homelab-ngx?q=stack-";
  const KINDS: [Kind, string][] = [
    ["git", "Git repository"],
    ["folder", "Local folder"],
    ["write", "Write it here"],
  ];

  const client = useQueryClient();
  let kind = $state<Kind>("git");
  let url = $state("");
  let branch = $state("");
  let folder = $state("");
  /** The name to install under; empty takes the one the stack suggests. */
  let chosenName = $state("");
  let error = $state("");
  let working = $state(false);
  /** The added stack; set once the daemon has read its source, or from ?stack= when a written stack continues here. */
  let name = $state<string | null>(page.url.searchParams.get("stack"));
  let installing = $state(false);
  interface PrivateKeyPrompt {
    source: string;
    publicKey: string;
    fingerprint: string;
    deployKeyUrl: string;
  }
  let privateKey = $state<PrivateKeyPrompt | null>(null);
  let copied = $state(false);

  const source = $derived(kind === "git" ? gitSource(url, branch) : kind === "folder" ? folderSource(folder) : null);
  const shownKey = $derived(privateKey && privateKey.source === source ? privateKey : null);
  const sourceHint = $derived(
    kind === "git"
      ? url.trim() && !source
        ? "Not a git URL. Use https://github.com/owner/repo, git@host:path or ssh://…"
        : ""
      : folder.trim() && !source
        ? "Not an absolute path."
        : "",
  );

  const nameHint = $derived(
    chosenName.trim() && !validStackName(chosenName.trim())
      ? "Up to 12 lowercase letters, digits and dashes, starting with a letter."
      : "",
  );

  const stack = createQuery(() => ({
    queryKey: [...keys.stacks, name ?? ""],
    queryFn: () => fetchStack(name!),
    enabled: name !== null,
    refetchInterval: (q) => (q.state.data && busy(q.state.data) ? busyPoll : false),
  }));
  const attempts = createQuery(() => ({
    queryKey: keys.updates(name ?? ""),
    queryFn: () => fetchUpdates(name!, 1),
    enabled: installing,
    refetchInterval: stack.data && busy(stack.data) ? busyPoll : false,
  }));

  const step = $derived(installing ? 2 : name ? 1 : 0);

  /** Starts a draft from the template and opens the editor. */
  async function write() {
    const n = chosenName.trim();
    if (!validStackName(n)) return;
    error = "";
    working = true;
    try {
      const d = await call<Draft>(api.POST("/drafts", { body: { name: n } }));
      client.setQueryData(keys.draft(d.name), d);
      await client.invalidateQueries({ queryKey: keys.drafts });
      await goto(`/stacks/drafts/${encodeURIComponent(d.name)}`);
    } catch (err) {
      error = describe(err);
    } finally {
      working = false;
    }
  }

  async function submitAdd() {
    if (!source || nameHint) return;
    error = "";
    working = true;
    try {
      const s = await call<Stack>(api.POST("/stacks", { body: { source, name: chosenName.trim() || undefined } }));
      privateKey = null;
      name = s.name;
      client.setQueryData([...keys.stacks, s.name], s);
      await client.invalidateQueries({ queryKey: keys.stacks });
    } catch (err) {
      if (err instanceof ProblemError && err.code === "private_repo" && typeof err.extra.public_key === "string" && err.extra.public_key) {
        privateKey = {
          source,
          publicKey: err.extra.public_key,
          fingerprint: typeof err.extra.fingerprint === "string" ? err.extra.fingerprint : "",
          deployKeyUrl: typeof err.extra.deploy_key_url === "string" ? err.extra.deploy_key_url : "",
        };
        copied = false;
      } else {
        privateKey = null;
        error =
          err instanceof ProblemError && err.code === "stack_exists"
            ? `${describe(err)}. Set another name below.`
            : describe(err);
      }
    } finally {
      working = false;
    }
  }

  async function copyKey() {
    if (!shownKey) return;
    try {
      if (navigator.clipboard) await navigator.clipboard.writeText(shownKey.publicKey);
      copied = true;
    } catch {
      copied = false;
    }
  }

  async function add(e: SubmitEvent) {
    e.preventDefault();
    if (kind === "write") return write();
    await submitAdd();
  }

  async function install(s: Stack) {
    error = "";
    working = true;
    try {
      const path = { params: { path: { name: s.name } } };
      await call(api.POST("/stacks/{name}/approve", { ...path, body: { rev: s.fetched.rev } }));
      await call(api.POST("/stacks/{name}/start", path));
      installing = true;
      await client.invalidateQueries({ queryKey: keys.all });
    } catch (err) {
      error = describe(err);
    } finally {
      working = false;
    }
  }
</script>

<svelte:head><title>Add a stack · Homelab-ngx</title></svelte:head>

<p class="text-muted"><a class="link" href="/stacks">Stacks</a> /</p>
<h1 class="mt-1 text-xl font-semibold">Add a stack</h1>

<ol class="mt-4 flex flex-wrap gap-2" aria-label="Steps">
  {#each STEPS as label, i (label)}
    <li
      aria-current={i === step ? "step" : undefined}
      class="rounded-full border px-3 py-0.5 text-xs {i === step
        ? 'border-accent font-semibold text-accent'
        : i < step
          ? 'border-line text-ok'
          : 'border-line text-muted'}"
    >
      {i + 1} · {label}
    </li>
  {/each}
</ol>

{#if error}
  <p role="alert" class="mt-4 rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
{/if}

{#if step === 0}
  <form class="mt-5 flex max-w-2xl flex-col gap-4" onsubmit={add}>
    <fieldset class="grid gap-3 sm:grid-cols-3">
      <legend class="label mb-2">
        <Help label="Source">
          A git repository set up as a homelab stack (private repositories use an SSH deploy key), a git checkout on this host, or two
          files written in the dashboard from an example.
        </Help>
      </legend>
      {#each KINDS as [value, label] (value)}
        <label class="card flex cursor-pointer items-center gap-3 has-checked:border-accent">
          <input type="radio" name="kind" {value} bind:group={kind} class="accent-accent" />
          {label}
        </label>
      {/each}
    </fieldset>

    {#if kind === "git"}
      <div class="grid gap-3 sm:grid-cols-[minmax(0,1fr)_12rem]">
        <label class="flex flex-col gap-1">
          <span class="label">URL</span>
          <input class="input" bind:value={url} placeholder="https://github.com/owner/homelab-paperless" autocomplete="off" spellcheck="false" required />
        </label>
        <label class="flex flex-col gap-1">
          <span class="label">Branch or tag (optional)</span>
          <input class="input" bind:value={branch} placeholder="default branch" autocomplete="off" spellcheck="false" />
        </label>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <span class="label">Official stacks</span>
        {#each STARTERS as s (s.name)}
          <button
            type="button"
            class="btn btn-sm {url === s.url ? 'border-accent text-accent' : ''}"
            onclick={() => {
              url = s.url;
              branch = "";
            }}
          >
            {s.label}
          </button>
        {/each}
        <a class="link text-xs" href={BROWSE_STACKS} target="_blank" rel="noopener noreferrer">Browse all</a>
      </div>
    {:else if kind === "folder"}
      <label class="flex flex-col gap-1">
        <span class="label">Path</span>
        <PathInput bind:value={folder} placeholder="/srv/stacks/paperless" required />
      </label>
    {/if}
    {#if sourceHint}<p class="text-warn">{sourceHint}</p>{/if}

    <div class="flex max-w-xs flex-col gap-1">
      <span class="label">
        <Help label={kind === "write" ? "Name" : "Name (optional)"}>
          The stack's services and folders are named after it, so it cannot change later; its label on the dashboard can.
          {#if kind !== "write"}Empty uses the stack's own name. Another name installs the same stack a second time.{/if}
        </Help>
      </span>
      <input
        class="input"
        bind:value={chosenName}
        aria-label="Name"
        placeholder={kind === "write" ? "mysite" : "the stack's own"}
        autocomplete="off"
        spellcheck="false"
        required={kind === "write"}
        aria-invalid={!!nameHint}
      />
    </div>
    {#if nameHint}<p class="text-warn">{nameHint}</p>{/if}

    <div>
      {#if kind === "write"}
        <button type="submit" class="btn btn-primary" disabled={!validStackName(chosenName.trim()) || working}>
          {working ? "Starting…" : "Start writing"}
        </button>
      {:else}
        <button type="submit" class="btn btn-primary" disabled={!source || !!nameHint || working}>{working ? "Reading…" : "Read the repo"}</button>
      {/if}
    </div>
  </form>
  {#if shownKey}
    <section class="card mt-5 max-w-2xl" aria-labelledby="deploy-key-h">
      <h2 id="deploy-key-h" class="font-semibold">Deploy key needed</h2>
      <p class="mt-1 text-muted">Add this read-only deploy key to the repository, then retry.</p>
      <pre class="mt-3 overflow-x-auto rounded-md bg-sunken p-3 text-xs">{shownKey.publicKey}</pre>
      {#if shownKey.fingerprint}<p class="mt-1.5 text-xs text-muted">{shownKey.fingerprint}</p>{/if}
      <div class="mt-3 flex flex-wrap items-center gap-2">
        <button type="button" class="btn" onclick={copyKey}>{copied ? "Copied" : "Copy key"}</button>
        {#if shownKey.deployKeyUrl}
          <a class="btn" href={shownKey.deployKeyUrl} target="_blank" rel="noopener noreferrer">Open deploy keys</a>
        {/if}
        <button type="button" class="btn btn-primary" disabled={working} onclick={submitAdd}>{working ? "Retrying…" : "Retry"}</button>
      </div>
    </section>
  {/if}
{:else if stack.data}
  {@const s = stack.data}
  {#if step === 1}
    {@const missing = missingVariables(s)}
    {@const asks = s.approved ? grants(s.fetched.manifest) : s.new_grants}
    <section class="card mt-5" aria-labelledby="asks-h">
      <h2 id="asks-h" class="font-semibold">
        <Help label="Access">
          What the stack may reach outside its own container: host folders, web addresses, ports, the GPU. Everything else is
          blocked, including the LAN, Tailscale and other ports on the host.
        </Help>
      </h2>
      <p class="mt-0.5 text-xs text-muted">
        {stackTitle(s)} · {draftOf(s.source) ? "written in the dashboard" : describeSource(s.source).repo} · {s.fetched.rev.slice(0, 7)}
      </p>
      <ul class="mt-2 divide-y divide-line">
        {#each asks as g (g.kind + g.detail)}
          <li class="flex gap-3 py-1.5">
            <span class="label w-20 shrink-0 pt-0.5">{g.kind}</span>
            <span class="break-all">{g.detail}</span>
          </li>
        {:else}
          <li class="py-1.5 text-muted">None. The stack reaches only its own network.</li>
        {/each}
      </ul>
      {#if s.source_dirty}<p class="mt-2 text-warn">Uncommitted changes in the folder are not installed.</p>{/if}
    </section>

    <div class="mt-4"><StackMappings stack={s} /></div>

    {#if !s.variables_ready}
      <p class="mt-4 text-warn">
        {missing.length ? `Required before install: ${missing.join(", ")}.` : "The stack's variables could not be read. See the daemon's log."}
      </p>
    {/if}
    <div class="mt-4 flex gap-2">
      <a class="btn" href="/stacks/{s.name}">Later</a>
      <button type="button" class="btn btn-primary" disabled={working || !s.variables_ready} onclick={() => install(s)}>Approve and install</button>
    </div>
  {:else if s.operation}
    <div class="mt-5 max-w-2xl"><OperationCard stack={s} attempt={attempts.data?.[0]} /></div>
    <a class="btn mt-3 {!busy(s) && !s.operation.error ? 'btn-primary' : ''}" href="/stacks/{s.name}">Open {s.name}</a>
  {/if}
{:else if name}
  <p class="mt-5 text-muted">Loading…</p>
{/if}
