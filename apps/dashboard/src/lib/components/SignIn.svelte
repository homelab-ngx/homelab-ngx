<script lang="ts">
  import { useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe } from "#lib/api/client.ts";
  import { newPasswordProblem } from "#lib/auth.ts";
  import { session } from "#lib/auth.svelte.ts";

  interface Props {
    /** No password yet: this form sets the first one. */
    setup: boolean;
  }
  let { setup }: Props = $props();

  const id = $props.id();
  const client = useQueryClient();
  let password = $state("");
  let again = $state("");
  let error = $state("");
  let pending = $state(false);
  const problem = $derived(!password ? "" : setup ? newPasswordProblem(password, again) : "");

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!password || problem) return;
    pending = true;
    error = "";
    try {
      const body = { password };
      await call(setup ? api.POST("/auth/setup", { body }) : api.POST("/auth/login", { body }));
      // Whatever was fetched while signed out was refused; fetch it again.
      client.clear();
      session.signedIn();
    } catch (err) {
      error = describe(err);
    } finally {
      pending = false;
    }
  }
</script>

<svelte:head><title>{setup ? "Set a password" : "Sign in"} · Homelab-ngx</title></svelte:head>

<main class="mx-auto grid max-w-sm gap-4 px-4 py-16">
  <h1 class="text-xl font-semibold">{setup ? "Set a password" : "Sign in"}</h1>
  {#if setup}<p class="text-muted">No password yet. The first one set here unlocks the dashboard.</p>{/if}
  <form id="{id}-form" class="card grid gap-4" onsubmit={submit}>
    <!-- For password managers: the dashboard has one user. -->
    <input type="text" name="username" autocomplete="username" value="homelab" hidden readonly />
    <div class="grid gap-1">
      <label for="{id}-pw" class="text-muted">{setup ? "New password" : "Password"}</label>
      <!-- svelte-ignore a11y_autofocus -->
      <input id="{id}-pw" type="password" class="input" autocomplete={setup ? "new-password" : "current-password"} autofocus bind:value={password} />
    </div>
    {#if setup}
      <div class="grid gap-1">
        <label for="{id}-again" class="text-muted">Again</label>
        <input id="{id}-again" type="password" class="input" autocomplete="new-password" bind:value={again} />
      </div>
    {/if}
    {#if problem}<p class="text-muted">{problem}</p>{/if}
    {#if error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
    <button type="submit" class="btn btn-primary justify-self-end" disabled={pending || !password || !!problem} title={problem || undefined}>
      {setup ? "Set password" : "Sign in"}
    </button>
  </form>
</main>
