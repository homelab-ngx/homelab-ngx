<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { ChevronRight } from "@lucide/svelte";
  import { api, call, describe } from "#lib/api/client.ts";
  import { fetchAssistant, keys } from "#lib/api/queries.ts";
  import { headline } from "#lib/components/core/core.ts";
  import Help from "#lib/components/Help.svelte";
  import PasswordForm from "#lib/components/PasswordForm.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { session } from "#lib/auth.svelte.ts";
  import { assistantLabel } from "#lib/assistant.ts";
  import { fetchHostSettings, hostKeys } from "#lib/host.ts";
  import { fetchAudits, POSTURE, RESULT, securityKeys } from "#lib/security.ts";
  import PowerDialog from "#lib/components/PowerDialog.svelte";
  import { fetchPower, powerKey, type PowerAction } from "#lib/power.ts";

  const assistant = createQuery(() => ({ queryKey: keys.assistant, queryFn: fetchAssistant }));
  const assistantOn = $derived(!!assistant.data && assistant.data.provider !== "none");
  const audits = createQuery(() => ({ queryKey: securityKeys.audits, queryFn: fetchAudits }));
  const lastAudit = $derived(audits.data?.[0]);

  // Only a host on the image answers this.
  const image = createQuery(() => ({
    queryKey: ["daemon", "system-updates", "image"],
    queryFn: () => call(api.GET("/system-updates/image")),
    retry: false,
  }));
  const updates = $derived(image.data ? headline(image.data) : undefined);

  const host = createQuery(() => ({ queryKey: hostKeys.settings, queryFn: fetchHostSettings }));
  const network = $derived.by(() => {
    const s = host.data;
    if (!s) return undefined;
    if (s.applied.errors.length > 0) return { tone: "bad" as const, state: "not applied" };
    return s.managed === "nixos" ? { tone: "info" as const, state: "nixos" } : { tone: "ok" as const, state: "applied" };
  });

  const power = createQuery(() => ({ queryKey: powerKey, queryFn: fetchPower }));
  const scheduled = $derived(power.data?.scheduled);
  let powerAction = $state<PowerAction | null>(null);

  let changing = $state(false);
  let signOutError = $state("");
  async function signOut() {
    signOutError = "";
    try {
      await session.signOut();
    } catch (e) {
      signOutError = describe(e);
    }
  }

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

<svelte:head><title>System Settings · Homelab-ngx</title></svelte:head>

<h1 class="text-xl font-semibold">System Settings</h1>

{#snippet row(name: string, href: string, core = false)}
  <a class="w-36 shrink-0 rounded-sm font-semibold hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent {core ? 'text-core' : ''}" {href}>{name}</a>
{/snippet}
{#snippet go()}<ChevronRight class="size-4 shrink-0 text-muted" aria-hidden="true" />{/snippet}

<div class="mt-5 grid max-w-3xl gap-4">
  <section class="overflow-hidden rounded-lg border border-line bg-surface" aria-labelledby="host-h">
    <h2 id="host-h" class="label bg-sunken px-3 py-2">This host</h2>
    <ul class="divide-y divide-line border-t border-line">
      <li class="flex min-h-11 items-center gap-x-3 px-3 py-2">
        {@render row("Updates", "/settings/updates", true)}
        <span class="flex min-w-0 grow flex-wrap items-center gap-x-2">
          {#if updates}<Pill tone={updates.tone}>{updates.state}</Pill>{#if updates.detail}<span class="text-muted max-sm:hidden">{updates.detail}</span>{/if}{/if}
        </span>
        {@render go()}
      </li>
      <li class="flex min-h-11 items-center gap-x-3 px-3 py-2">
        {@render row("Network", "/settings/network")}
        <span class="flex min-w-0 grow flex-wrap items-center gap-x-2">
          {#if network}<Pill tone={network.tone}>{network.state}</Pill>{/if}
          {#if host.data?.domain.domain}<span class="truncate text-muted max-sm:hidden">{host.data.domain.domain}</span>{/if}
        </span>
        {@render go()}
      </li>
      <li class="flex min-h-11 items-center gap-x-3 px-3 py-2">
        {@render row("Logs", "/settings/logs")}
        <span class="grow text-muted">Host, backups</span>
        {@render go()}
      </li>
      <li class="flex min-h-11 flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2">
        <span class="flex grow flex-wrap items-center gap-x-3 font-semibold">
          <Help label="Power">
            Before the host goes down the stacks stop and the pools unmount. The next boot starts the same stacks and mounts the
            same pools.
          </Help>
          {#if scheduled}
            <span class="font-normal text-muted">{scheduled.action} at {new Date(scheduled.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</span>
          {/if}
        </span>
        <span class="flex flex-wrap gap-2">
          <button type="button" class="btn btn-sm" onclick={() => (powerAction = "reboot")}>Reboot…</button>
          <button type="button" class="btn btn-sm" onclick={() => (powerAction = "poweroff")}>Shut down…</button>
        </span>
      </li>
    </ul>
  </section>

  <section class="overflow-hidden rounded-lg border border-line bg-surface" aria-labelledby="assistant-h">
    <h2 id="assistant-h" class="label bg-sunken px-3 py-2">Assistant</h2>
    <ul class="divide-y divide-line border-t border-line">
      <li class="flex min-h-11 items-center gap-x-3 px-3 py-2">
        {@render row("AI assistant", "/settings/assistant")}
        <span class="grow">{#if assistant.data}<Pill tone={assistantOn ? "ok" : "idle"}>{assistantLabel(assistant.data)}</Pill>{/if}</span>
        {@render go()}
      </li>
      <li class="flex min-h-11 items-center gap-x-3 px-3 py-2">
        {@render row("Security audit", "/settings/security")}
        <span class="grow">
          {#if lastAudit?.posture}<Pill tone={POSTURE[lastAudit.posture]}>{lastAudit.posture}</Pill>
          {:else if lastAudit}<Pill tone={RESULT[lastAudit.result].tone}>{RESULT[lastAudit.result].label}</Pill>
          {:else if audits.data}<Pill tone="idle">never</Pill>{/if}
        </span>
        {@render go()}
      </li>
    </ul>
  </section>

  <section class="overflow-hidden rounded-lg border border-line bg-surface" aria-labelledby="browser-h">
    <h2 id="browser-h" class="label bg-sunken px-3 py-2">This browser</h2>
    <ul class="divide-y divide-line border-t border-line">
      <li class="flex min-h-11 flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2">
        <span class="flex grow flex-wrap font-semibold">
          <Help label="Sign-in">
            One password unlocks the dashboard. A forgotten one is removed on the host with homelab auth reset; the next visitor sets
            a new one.
          </Help>
        </span>
        <span class="flex flex-wrap gap-2">
          <button type="button" class="btn btn-sm" onclick={() => (changing = true)}>Change password…</button>
          <button type="button" class="btn btn-sm" onclick={signOut}>Sign out</button>
        </span>
      </li>
      <li class="flex min-h-11 flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2">
        <span id="theme-h" class="grow font-semibold">Theme</span>
        <span class="flex gap-2" role="radiogroup" aria-labelledby="theme-h">
          {#each ["system", "light", "dark"] as const as t (t)}
            <button type="button" role="radio" aria-checked={theme === t} class="btn btn-sm aria-checked:border-accent aria-checked:text-accent" onclick={() => setTheme(t)}>
              {t[0]!.toUpperCase() + t.slice(1)}
            </button>
          {/each}
        </span>
      </li>
    </ul>
  </section>
  {#if signOutError}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{signOutError}</p>{/if}
</div>

{#if changing}<PasswordForm onClose={() => (changing = false)} />{/if}
{#if powerAction}<PowerDialog action={powerAction} onClose={() => (powerAction = null)} />{/if}
