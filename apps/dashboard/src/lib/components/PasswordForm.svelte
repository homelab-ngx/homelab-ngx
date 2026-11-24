<script lang="ts">
  import { api, call, describe } from "#lib/api/client.ts";
  import { newPasswordProblem } from "#lib/auth.ts";

  const id = $props.id();
  let current = $state("");
  let password = $state("");
  let again = $state("");
  let error = $state("");
  let done = $state(false);
  let pending = $state(false);
  const problem = $derived(!current ? "Enter the current password." : newPasswordProblem(password, again));

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (problem) return;
    pending = true;
    error = "";
    done = false;
    try {
      await call(api.PUT("/auth/password", { body: { current_password: current, password } }));
      current = password = again = "";
      done = true;
    } catch (err) {
      error = describe(err);
    } finally {
      pending = false;
    }
  }
</script>

<section class="card" aria-labelledby="{id}-h">
  <h2 id="{id}-h" class="font-semibold">Password</h2>
  <p class="text-xs text-muted">Changing it signs out every other browser.</p>
  <form class="mt-3 grid max-w-sm gap-3" onsubmit={submit}>
    <input type="text" name="username" autocomplete="username" value="homelab" hidden readonly />
    <div class="grid gap-1">
      <label for="{id}-current" class="text-muted">Current password</label>
      <input id="{id}-current" type="password" class="input" autocomplete="current-password" bind:value={current} />
    </div>
    <div class="grid gap-1">
      <label for="{id}-new" class="text-muted">New password</label>
      <input id="{id}-new" type="password" class="input" autocomplete="new-password" bind:value={password} />
    </div>
    <div class="grid gap-1">
      <label for="{id}-again" class="text-muted">Again</label>
      <input id="{id}-again" type="password" class="input" autocomplete="new-password" bind:value={again} />
    </div>
    {#if problem && password}<p class="text-muted">{problem}</p>{/if}
    {#if error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
    {#if done}<p role="status" class="rounded-md bg-ok-soft px-3 py-2 text-ok">Password changed.</p>{/if}
    <div class="flex justify-end">
      <button type="submit" class="btn btn-primary" disabled={pending || !!problem} title={problem || undefined}>Change</button>
    </div>
  </form>
</section>
