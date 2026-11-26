<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { untrack } from "svelte";
  import { api, call, describe, type AssistantModels, type AssistantSettings, type AssistantTest } from "#lib/api/client.ts";
  import { fetchAssistantLimits, keys } from "#lib/api/queries.ts";
  import { dateTime } from "#lib/format.ts";
  import { CLAUDE_MODELS, LIMIT_WINDOWS, OPENAI_EXAMPLES, usageLabel } from "#lib/assistant.ts";
  import Help from "./Help.svelte";
  import Pill from "./Pill.svelte";

  interface Props {
    /** The stored setup; the form edits its own copy. */
    current: AssistantSettings;
  }
  let { current }: Props = $props();

  const client = useQueryClient();
  const start = untrack(() => current);
  let provider = $state<AssistantSettings["provider"]>(start.provider === "none" ? "anthropic" : start.provider);
  const enabled = $derived(current.provider !== "none");
  let model = $state(start.model || CLAUDE_MODELS[0]!);
  let auditModel = $state(start.audit_model);
  let baseUrl = $state(start.base_url);
  let apiKey = $state("");
  let error = $state("");
  let saved = $state("");
  let working = $state<"" | "save" | "test" | "off">("");
  let test = $state<AssistantTest | null>(null);
  /** The server's model ids, once listed; null before. */
  let models = $state<string[] | null>(null);
  let modelsError = $state("");
  let listing = $state(false);

  /** Spent allowance, for a server that reports it (OpenCode Go). */
  const limits = createQuery(() => ({
    queryKey: keys.assistantLimits,
    queryFn: fetchAssistantLimits,
    enabled: current.provider === "openai",
    retry: false,
  }));
  const suggestions = $derived(models ?? (provider === "anthropic" ? CLAUDE_MODELS : []));

  /** A key is stored for the provider shown, so the field may stay empty. */
  const keyKept = $derived(current.key_set && current.provider === provider);
  const needsKey = $derived(provider === "anthropic" && !keyKept && !apiKey.trim() && !baseUrl.trim());

  /** What the form shows for the stored setup; Reset returns to it. */
  const stored = $derived({
    provider: current.provider === "none" ? ("anthropic" as const) : current.provider,
    model: current.model || CLAUDE_MODELS[0]!,
    auditModel: current.audit_model,
    baseUrl: current.base_url,
  });
  const changed = $derived(
    provider !== stored.provider ||
      model.trim() !== stored.model ||
      auditModel.trim() !== stored.auditModel ||
      baseUrl.trim() !== stored.baseUrl ||
      !!apiKey.trim(),
  );
  /** Why Apply is disabled, when a field is missing. */
  const blocked = $derived(needsKey ? "Claude needs an API key." : provider === "openai" && !model.trim() ? "A model name is needed." : "");

  function reset() {
    provider = stored.provider;
    model = stored.model;
    auditModel = stored.auditModel;
    baseUrl = stored.baseUrl;
    apiKey = "";
    error = saved = "";
  }

  function pick(p: "anthropic" | "openai") {
    if (p === provider) return;
    provider = p;
    // Each provider has its own models and address.
    model = p === "anthropic" ? CLAUDE_MODELS[0]! : "";
    auditModel = p === current.provider ? current.audit_model : "";
    baseUrl = p === current.provider ? current.base_url : "";
    test = null;
    models = null;
    modelsError = "";
  }

  /** Lists the server's models with the address and key in the form, applied or not. */
  async function listModels() {
    modelsError = "";
    listing = true;
    try {
      const r = await call<AssistantModels>(
        api.POST("/settings/assistant/models", {
          body: { provider: provider === "openai" ? "openai" : "anthropic", base_url: baseUrl.trim(), api_key: apiKey.trim() ? apiKey.trim() : undefined },
        }),
      );
      models = r.models;
      if (!r.models.length) modelsError = "The server lists no models.";
    } catch (err) {
      modelsError = describe(err);
    } finally {
      listing = false;
    }
  }

  async function save(e: SubmitEvent) {
    e.preventDefault();
    error = saved = "";
    working = "save";
    try {
      const s = await call<AssistantSettings>(
        api.PUT("/settings/assistant", {
          body: {
            provider,
            model: model.trim(),
            audit_model: auditModel.trim(),
            base_url: baseUrl.trim(),
            api_key: apiKey.trim() ? apiKey.trim() : undefined,
          },
        }),
      );
      client.setQueryData(keys.assistant, s);
      void client.invalidateQueries({ queryKey: keys.assistantLimits });
      apiKey = "";
      saved = "Applied. Not tested yet.";
      test = null;
    } catch (err) {
      error = describe(err);
    } finally {
      working = "";
    }
  }

  async function runTest() {
    error = saved = "";
    test = null;
    working = "test";
    try {
      test = await call<AssistantTest>(api.POST("/settings/assistant/test"));
      void client.invalidateQueries({ queryKey: keys.assistantLimits });
    } catch (err) {
      error = describe(err);
    } finally {
      working = "";
    }
  }

  async function turnOff() {
    error = saved = "";
    working = "off";
    try {
      const s = await call<AssistantSettings>(api.PUT("/settings/assistant", { body: { provider: "none" } }));
      client.setQueryData(keys.assistant, s);
      test = null;
      saved = "Off. The key is deleted.";
    } catch (err) {
      error = describe(err);
    } finally {
      working = "";
    }
  }
</script>

<form class="mt-5 flex max-w-2xl flex-col gap-4" onsubmit={save}>
  <fieldset class="grid gap-3 sm:grid-cols-2">
    <legend class="label mb-2">
      <Help label="Provider">
        Sends the open and changed stack files, manifest, flake.nix, Check and Build output, and a writing guide, or logs
        for Ask. Never sends stack variable values.
      </Help>
    </legend>
    <label class="card flex cursor-pointer gap-3 has-checked:border-accent has-checked:bg-accent-soft">
      <input type="radio" name="provider" checked={provider === "anthropic"} onchange={() => pick("anthropic")} class="mt-1 accent-accent" />
      <span>
        Claude
        <span class="mt-0.5 block text-xs text-muted">Anthropic API key, pay per use</span>
      </span>
    </label>
    <label class="card flex cursor-pointer gap-3 has-checked:border-accent has-checked:bg-accent-soft">
      <input type="radio" name="provider" checked={provider === "openai"} onchange={() => pick("openai")} class="mt-1 accent-accent" />
      <span>
        OpenAI-compatible
        <span class="mt-0.5 block text-xs text-muted">OpenAI, OpenRouter, Ollama, LM Studio, vLLM</span>
      </span>
    </label>
  </fieldset>

  <div class="flex flex-col gap-1">
    <span class="label">
      <Help label={provider === "anthropic" ? "Address (optional)" : "Address"}>
        {#if provider === "anthropic"}
          Empty for Anthropic's own API. Set it for a proxy or gateway in front of it.
        {:else}
          Where the server answers, ending in /v1 for most.
          {#each OPENAI_EXAMPLES as ex (ex.name)}<br />{ex.name}: {ex.url}{/each}
        {/if}
      </Help>
    </span>
    <input
      class="input"
      aria-label="Address"
      bind:value={baseUrl}
      placeholder={provider === "anthropic" ? "https://api.anthropic.com" : "https://api.openai.com/v1"}
      autocomplete="off"
      spellcheck="false"
    />
  </div>

  <div class="flex max-w-md flex-col gap-1">
    <span class="label">
      <Help label="Model">The model's name as the server knows it. A small local model may struggle with Nix.</Help>
    </span>
    <div class="flex gap-2">
      <input
        class="input min-w-0 flex-1"
        aria-label="Model"
        bind:value={model}
        list="assistant-models"
        placeholder={provider === "anthropic" ? CLAUDE_MODELS[0] : "llama3.1, gpt-5, qwen2.5-coder…"}
        autocomplete="off"
        spellcheck="false"
        required={provider === "openai"}
      />
      <button type="button" class="btn" disabled={listing || !!working} onclick={listModels}>{listing ? "Listing…" : "List"}</button>
    </div>
    <datalist id="assistant-models">
      {#each suggestions as m (m)}<option value={m}></option>{/each}
    </datalist>
    {#if modelsError}<p class="text-xs break-words text-bad">{modelsError}</p>
    {:else if models}<p class="text-xs text-muted">{models.length} models</p>{/if}
  </div>

  <div class="flex max-w-md flex-col gap-1">
    <span class="label">
      <Help label="Audit model (optional)">
        A stronger model that reasons, for the security audit. Same provider and key. Empty uses the model above.
      </Help>
    </span>
    <input
      class="input"
      aria-label="Audit model"
      bind:value={auditModel}
      list="assistant-models"
      placeholder={model.trim() || "Same as the model"}
      autocomplete="off"
      spellcheck="false"
    />
  </div>

  <div class="flex max-w-md flex-col gap-1">
    <span class="label">
      <Help label={provider === "openai" ? "API key (optional)" : "API key"}>
        Stored encrypted on the server and never shown again. Typing a new one replaces it.
      </Help>
    </span>
    <input class="input" type="password" aria-label="API key" bind:value={apiKey} placeholder={keyKept ? "Stored" : ""} autocomplete="off" />
  </div>

  {#if error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
  {#if saved}<p role="status" class="text-ok">{saved}</p>{/if}
  {#if blocked}<p class="text-xs text-muted">{blocked}</p>{/if}

  <div class="flex flex-wrap items-center gap-2">
    <button type="submit" class="btn btn-primary" title={blocked || undefined} disabled={!!working || !!blocked || !(changed || current.provider === "none")}>
      {working === "save" ? "Applying…" : "Apply"}
    </button>
    <button type="button" class="btn" disabled={!!working || !changed} onclick={reset}>Reset</button>
    {#if enabled}
      <button type="button" class="btn" disabled={!!working} onclick={runTest}>{working === "test" ? "Asking…" : "Test"}</button>
      <button type="button" class="btn btn-danger" disabled={!!working} onclick={turnOff}>Turn off</button>
    {/if}
  </div>

  {#if test}
    <div class="card" role="status">
      <div class="flex flex-wrap items-center gap-2">
        <Pill tone={test.ok ? "ok" : "bad"}>{test.ok ? "answers" : "no answer"}</Pill>
        <span class="text-xs text-muted">{[test.model, `${test.seconds} s`, usageLabel(test.usage)].filter(Boolean).join(" · ")}</span>
      </div>
      {#if test.reply}<p class="mt-2 text-muted">“{test.reply}”</p>{/if}
      {#if test.error}<p class="mt-2 break-words text-bad">{test.error}</p>{/if}
    </div>
  {/if}

  {#if enabled && limits.data?.limits.length}
    <div class="card" role="group" aria-labelledby="limits-h">
      <h2 id="limits-h" class="label">
        <Help label="Usage">The share of the plan's allowance spent in each window, as the provider reports it.</Help>
      </h2>
      <ul class="mt-2 flex flex-col gap-1.5">
        {#each limits.data.limits as l (l.window)}
          <li class="grid grid-cols-[5rem_auto_1fr] items-center gap-3">
            <span>{LIMIT_WINDOWS[l.window] ?? l.window}</span>
            <Pill tone={l.status === "ok" ? "ok" : "warn"}>{l.status}</Pill>
            <span class="min-w-0 text-muted tabular-nums">
              {Math.round(l.percent)}%{#if l.resets_at}{" · "}resets {dateTime(l.resets_at, { weekday: "short", hour: "2-digit", minute: "2-digit" })}{/if}
            </span>
          </li>
        {/each}
      </ul>
    </div>
  {:else if enabled && limits.error}
    <p class="text-xs break-words text-muted">Usage: {describe(limits.error)}</p>
  {/if}
</form>
