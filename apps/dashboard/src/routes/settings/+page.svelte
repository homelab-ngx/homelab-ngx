<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { fetchAssistant, keys } from "#lib/api/queries.ts";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { assistantLabel } from "#lib/assistant.ts";
  import { fetchAudits, POSTURE, RESULT, securityKeys } from "#lib/security.ts";

  const assistant = createQuery(() => ({ queryKey: keys.assistant, queryFn: fetchAssistant }));
  const assistantOn = $derived(!!assistant.data && assistant.data.provider !== "none");
  const audits = createQuery(() => ({ queryKey: securityKeys.audits, queryFn: fetchAudits }));
  const lastAudit = $derived(audits.data?.[0]);

  type Theme = "system" | "light" | "dark";
  const read = (): Theme => {
    try {
      return (localStorage.getItem("theme") as Theme | null) ?? "system";
    } catch {
      return "system";
    }
  };
  let theme = $state<Theme>(read());

  function setTheme(t: Theme) {
    theme = t;
    try {
      localStorage.setItem("theme", t);
    } catch {
      // A private window: the choice lasts for this page only.
    }
    const dark = t === "dark" || (t === "system" && matchMedia("(prefers-color-scheme: dark)").matches);
    document.documentElement.classList.toggle("dark", dark);
  }
</script>

<svelte:head><title>Settings · Homelab-ngx</title></svelte:head>

<h1 class="text-xl font-semibold">Settings</h1>

<div class="mt-5 grid max-w-3xl gap-4">
  <section class="card border-core/40" aria-labelledby="core-h">
    <h2 id="core-h" class="font-semibold text-core">
      <Help label="Core host">The OS and the few things that must always work. It changes rarely, and stacks never change it.</Help>
    </h2>
    <p class="mt-1 text-muted">Release, updates, reboot.</p>
    <a class="btn mt-3" href="/settings/core">Open core host</a>
  </section>

  <section class="card" aria-labelledby="host-h">
    <h2 id="host-h" class="font-semibold">
      <Help label="Host">The host's domain and certificate, LAN, firewall, VPN, tunnel and Tailscale.</Help>
    </h2>
    <p class="mt-1 text-muted">Domain, VPN, firewall, tunnel.</p>
    <a class="btn mt-3" href="/settings/host">Open host</a>
  </section>

  <section class="card" aria-labelledby="assistant-h">
    <div class="flex flex-wrap items-center gap-2">
      <h2 id="assistant-h" class="font-semibold">
        <Help label="AI assistant">
          Optional. With Claude or a self-run model connected, the stack editor can draft a stack from a description, review one
          and fix a failed build.
        </Help>
      </h2>
      {#if assistant.data}<Pill tone={assistantOn ? "ok" : "idle"}>{assistantLabel(assistant.data)}</Pill>{/if}
    </div>
    <a class="btn mt-3" href="/settings/assistant">{assistantOn ? "Change" : "Set up"}</a>
  </section>

  <section class="card" aria-labelledby="audit-h">
    <div class="flex flex-wrap items-center gap-2">
      <h2 id="audit-h" class="font-semibold">
        <Help label="Security audit">
          The assistant's audit model reviews the host's exposure, access, stacks, secrets and updates, and says what to fix
          first.
        </Help>
      </h2>
      {#if lastAudit?.posture}<Pill tone={POSTURE[lastAudit.posture]}>{lastAudit.posture}</Pill>
      {:else if lastAudit}<Pill tone={RESULT[lastAudit.result].tone}>{RESULT[lastAudit.result].label}</Pill>
      {:else if audits.data}<Pill tone="idle">never</Pill>{/if}
    </div>
    <a class="btn mt-3" href="/settings/security">Open audit</a>
  </section>

  <section class="card" aria-labelledby="logs-h">
    <h2 id="logs-h" class="font-semibold">Logs</h2>
    <p class="mt-1 text-muted">Host and backup jobs.</p>
    <a class="btn mt-3" href="/settings/logs">Open logs</a>
  </section>

  <section class="card" aria-labelledby="theme-h">
    <h2 id="theme-h" class="font-semibold">Theme</h2>
    <div class="mt-2 flex gap-2" role="radiogroup" aria-labelledby="theme-h">
      {#each ["system", "light", "dark"] as const as t (t)}
        <button type="button" role="radio" aria-checked={theme === t} class="btn aria-checked:border-accent aria-checked:text-accent" onclick={() => setTheme(t)}>
          {t[0]!.toUpperCase() + t.slice(1)}
        </button>
      {/each}
    </div>
  </section>
</div>
