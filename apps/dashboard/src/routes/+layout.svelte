<script lang="ts">
  import "../app.css";
  import { QueryClient, QueryClientProvider } from "@tanstack/svelte-query";
  import type { Snippet } from "svelte";
  import { page } from "$app/state";
  import { NAV, SETTINGS, current } from "#lib/nav.ts";
  import Shell from "#lib/components/Shell.svelte";
  import Installer from "#lib/components/installer/Installer.svelte";
  import SignIn from "#lib/components/SignIn.svelte";
  import { api, call, type Schema } from "#lib/api/client.ts";
  import { session } from "#lib/auth.svelte.ts";

  let { children }: { children: Snippet } = $props();

  const client = new QueryClient({
    defaultOptions: { queries: { staleTime: 5_000, retry: 1, refetchIntervalInBackground: false } },
  });

  // One password unlocks the dashboard; until it is given, the sign-in
  // screen is all the page shows.
  $effect(() => {
    void session.check();
  });

  // The installer stick's daemon serves only the installer: the page is
  // all it shows. Any other host answers that the installer is not active.
  let installer = $state<Schema<"InstallerState"> | null>(null);
  let ready = $state(false);
  $effect(() => {
    call(api.GET("/installer"))
      .then((s) => (installer = s.active ? s : null))
      .catch(() => (installer = null))
      .finally(() => (ready = true));
  });

  $effect(() => {
    // Makes the dashboard installable; the worker passes every request through.
    if ("serviceWorker" in navigator) {
      navigator.serviceWorker.register("/sw.js").catch((e: unknown) => console.warn("service worker registration failed", e));
    }
  });
</script>

<QueryClientProvider {client}>
  {#if session.gate === "setup" || session.gate === "login"}
    <SignIn setup={session.gate === "setup"} />
  {:else if session.gate === "checking"}
    <!-- Not known yet. -->
  {:else if installer}
    <Installer initial={installer} />
  {:else if ready}
    <Shell active={current(page.url.pathname)} nav={NAV} settings={SETTINGS} wide={page.url.pathname.startsWith("/stacks/drafts/")}>
      {@render children()}
    </Shell>
  {/if}
</QueryClientProvider>
