<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { Bell, CircleQuestionMark, Settings } from "@lucide/svelte";
  import type { Snippet } from "svelte";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { api, call, DEMO } from "#lib/api/client.ts";
  import { fetchStacks, idlePoll, keys } from "#lib/api/queries.ts";
  import { clock } from "#lib/clock.svelte.ts";
  import NotificationsDialog from "#lib/components/NotificationsDialog.svelte";
  import StatusMark from "#lib/components/StatusMark.svelte";
  import { help, toggleHelp } from "#lib/help.svelte.ts";
  import { fetchHostSettings, hostKeys } from "#lib/host.ts";
  import { liveNotifications } from "#lib/live-notifications.svelte.ts";
  import type { NavItem } from "#lib/nav.ts";
  import { notificationsTray } from "#lib/notifications-tray.svelte.ts";
  import { attention, notificationKeys, problemCount, type Snapshot } from "#lib/notifications.ts";
  import { fetchPower, powerHref, powerKey, underWay } from "#lib/power.ts";
  import { fetchSettingsStatus, settingsAttentionTone, settingsStatusKey } from "#lib/settings.ts";

  interface Props {
    active: string;
    nav: NavItem[];
    settings: NavItem;
    /** A workbench page (the stack editor): the full width, little margin. */
    wide?: boolean;
    children: Snippet;
  }
  let { active, nav, settings, wide = false, children }: Props = $props();

  // The host's own name heads every page, and its clock format is every
  // time's; saving them on the General page updates this query's data.
  const host = createQuery(() => ({ queryKey: hostKeys.settings, queryFn: fetchHostSettings }));
  $effect(() => {
    clock.format = host.data?.time.clock ?? "";
  });
  const hostname = $derived(host.data?.host.hostname);
  const shortName = $derived(hostname && hostname.length > 16 ? `${hostname.slice(0, 16)}…` : hostname);

  const stacks = createQuery(() => ({ queryKey: keys.stacks, queryFn: fetchStacks, refetchInterval: idlePoll }));

  // A shutdown started anywhere (the CLI, the power button, an update's
  // reboot) takes every page to the power page.
  const power = createQuery(() => ({ queryKey: powerKey, queryFn: fetchPower, refetchInterval: idlePoll, retry: false }));
  $effect(() => {
    const s = power.data;
    if (!s || !underWay(s) || page.url.pathname === "/power") return;
    void goto(powerHref(s.action ?? "reboot", s.boot_id, page.url.pathname));
  });
  function onkeydown(e: KeyboardEvent) {
    if (e.key !== "F1") return;
    e.preventDefault();
    toggleHelp();
  }

  const client = useQueryClient();
  $effect(() => {
    liveNotifications.open(client);
    return () => liveNotifications.close();
  });

  // The bell counts open problems, failing channels, and unread history entries.
  const notes = createQuery(() => ({
    queryKey: notificationKeys.snapshot,
    queryFn: () => call<Snapshot>(api.GET("/notifications")),
    refetchInterval: () => (liveNotifications.connected ? false : 30_000),
  }));
  const problems = $derived(problemCount(notes.data));
  const count = $derived(attention(notes.data));

  const settingsStatus = createQuery(() => ({
    queryKey: settingsStatusKey,
    queryFn: fetchSettingsStatus,
    refetchInterval: 30_000,
  }));
  const settingsTone = $derived(settingsAttentionTone(settingsStatus.data));
  const settingsHint = $derived(settingsTone === "bad" ? "needs attention" : settingsTone === "warn" ? "warning" : settingsTone === "info" ? "update ready" : "");

  const pending = $derived((stacks.data ?? []).filter((s) => s.generation > 0 && s.update_state !== "up_to_date").length);
</script>

<svelte:window {onkeydown} />

<div class="flex min-h-dvh flex-col">
  {#if DEMO}
    <p class="bg-accent-soft px-4 py-1 text-center text-xs text-accent">Demo. Dummy data; reloading resets it.</p>
  {/if}
  <header class="sticky top-0 z-30 min-w-0 border-b border-line bg-surface/95 backdrop-blur">
    <nav aria-label="Main" class="mx-auto flex max-w-6xl items-center gap-1 px-4 py-2">
      <!-- The page links scroll sideways on a phone; Help and Settings stay on screen. -->
      <div class="relative flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
      {#if hostname}
        <a href="/" class="mr-1 shrink-0 whitespace-nowrap font-semibold tracking-tight sm:mr-3" title={shortName === hostname ? undefined : hostname}>{shortName}</a>
      {/if}
      {#each nav as item (item.href)}
        <a
          href={item.href}
          aria-current={active === item.href ? "page" : undefined}
          class="shrink-0 rounded-md px-2 py-1 sm:px-2.5 text-muted hover:text-fg aria-[current=page]:bg-accent-soft aria-[current=page]:font-semibold aria-[current=page]:text-accent"
        >
          {item.label}
          {#if item.href === "/stacks" && pending > 0}
            <span class="ml-1 rounded-full bg-warn-soft px-1.5 text-xs text-warn" aria-label="{pending} stacks with updates">{pending}</span>
          {/if}
        </a>
      {/each}
      </div>
      <button
        type="button"
        class="relative flex shrink-0 items-center rounded-md py-1 pr-3 pl-2 text-muted hover:text-fg focus-visible:outline-2 focus-visible:outline-accent aria-expanded:bg-accent-soft aria-expanded:text-accent"
        aria-label={count > 0 ? (problems > 0 ? `Notifications, ${count} need attention` : `Notifications, ${count} unread`) : "Notifications"}
        aria-expanded={notificationsTray.open}
        onclick={() => (notificationsTray.open = !notificationsTray.open)}
      >
        <span class="relative">
          <Bell class="size-4" aria-hidden="true" />
          {#if count > 0}<span class="absolute -top-1.5 left-full ml-px text-xs leading-none font-semibold tabular-nums {problems > 0 ? 'text-bad' : 'text-accent'}" aria-hidden="true">{count}</span>{/if}
        </span>
      </button>
      <button
        type="button"
        class="relative flex shrink-0 items-center gap-1 rounded-md px-2 py-1 text-xs text-muted hover:text-fg focus-visible:outline-2 focus-visible:outline-accent aria-pressed:bg-accent-soft aria-pressed:text-accent"
        aria-pressed={help.all}
        title="Show the help under every label (F1). A single label unfolds its own when clicked."
        onclick={toggleHelp}
      >
        <CircleQuestionMark class="size-4" aria-hidden="true" />
        <span class="max-sm:sr-only">Help</span>
      </button>
      <a
        href={settings.href}
        aria-current={active === settings.href ? "page" : undefined}
        class="relative flex shrink-0 items-center gap-1.5 border-l border-line pl-2 text-muted hover:text-fg aria-[current=page]:font-semibold aria-[current=page]:text-core sm:pl-3"
      >
        <!-- A phone has no room for the word; the gear keeps Settings on screen. -->
        <Settings class="size-4 sm:hidden" aria-hidden="true" />
        <span class="max-sm:sr-only">{settings.label}</span>
        {#if settingsTone}
          <StatusMark tone={settingsTone} />
          <span class="sr-only">, {settingsHint}</span>
        {/if}
      </a>
    </nav>
  </header>
  <main class="mx-auto w-full min-w-0 flex-1 {wide ? 'px-2 py-2 sm:px-3' : 'max-w-6xl px-4 py-6'}">
    {@render children()}
  </main>
  <footer class="px-4 py-4 text-center text-xs text-muted">homelab-ngx</footer>
</div>

{#if notificationsTray.open}<NotificationsDialog snapshot={notes.data} onClose={() => (notificationsTray.open = false)} />{/if}
