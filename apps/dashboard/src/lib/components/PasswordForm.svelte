<script lang="ts">
  import { api, call, describe } from "#lib/api/client.ts";
  import { newPasswordProblem } from "#lib/auth.ts";
  import Modal from "#lib/components/Modal.svelte";

  interface Props {
    onClose: () => void;
  }
  let { onClose }: Props = $props();

  const id = $props.id();
  let current = $state("");
  let password = $state("");
  let again = $state("");
  let error = $state("");
  let pending = $state(false);
  const problem = $derived(!current ? "Enter the current password." : newPasswordProblem(password, again));

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (problem) return;
    pending = true;
    error = "";
    try {
      await call(api.PUT("/auth/password", { body: { current_password: current, password } }));
      onClose();
    } catch (err) {
      error = describe(err);
    } finally {
      pending = false;
    }
  }
</script>

<Modal open={true} onOpenChange={(o) => !o && onClose()} title="Change password" description="Every other browser is signed out.">
  <form id="{id}-form" class="grid gap-4" onsubmit={submit}>
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
  </form>
  {#snippet actions()}
    <button type="submit" form="{id}-form" class="btn btn-primary" disabled={pending || !!problem} title={problem || undefined}>Change</button>
  {/snippet}
</Modal>
