<script lang="ts">
  import { describe } from "#lib/api/client.ts";
  import { session } from "#lib/auth.svelte.ts";
  import PasswordForm from "#lib/components/PasswordForm.svelte";
  import SettingsHead from "#lib/components/settings/SettingsHead.svelte";

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

<SettingsHead title="Account">
  {#snippet help()}The dashboard's one account. The password is the same in every browser; the theme belongs to this browser.{/snippet}
</SettingsHead>

<div class="mt-5 grid max-w-4xl gap-4">
  <PasswordForm />

  <section class="card" aria-labelledby="browser-h">
    <h2 id="browser-h" class="font-semibold">This browser</h2>
    <div class="mt-3 flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <span id="theme-h" class="text-muted">Theme</span>
        <span class="flex overflow-hidden rounded-md border border-line" role="radiogroup" aria-labelledby="theme-h">
          {#each ["system", "light", "dark"] as const as t (t)}
            <button
              type="button"
              role="radio"
              aria-checked={theme === t}
              class="px-2.5 py-1 not-first:border-l not-first:border-line hover:bg-sunken focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent aria-checked:bg-accent-soft aria-checked:text-accent"
              onclick={() => setTheme(t)}
            >
              {t[0]!.toUpperCase() + t.slice(1)}
            </button>
          {/each}
        </span>
      </div>
      <button type="button" class="btn" onclick={signOut}>Sign out</button>
    </div>
    {#if signOutError}<p role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">{signOutError}</p>{/if}
  </section>
</div>
