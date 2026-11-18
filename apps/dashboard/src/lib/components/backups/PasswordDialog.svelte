<script lang="ts">
  import { api, call, describe } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";
  import type { Destination } from "#lib/backups.ts";

  interface Props {
    destination: Destination;
    /** The password the daemon just generated. Without it the operator asks to see the stored one. */
    generated?: string;
    /** Called when the dialog is done; saved is true when the password was confirmed as saved. */
    onDone: (saved: boolean) => void;
  }
  let { destination, generated = "", onDone }: Props = $props();

  const id = $props.id();
  let revealed = $state("");
  let typed = $state("");
  let error = $state("");
  let pending = $state(false);
  let copied = $state(false);

  const password = $derived(generated || revealed);
  const mustConfirm = $derived(!destination.password_confirmed);
  const at = () => ({ params: { path: { id: destination.id } } });

  async function reveal(e: SubmitEvent) {
    e.preventDefault();
    pending = true;
    error = "";
    try {
      revealed = (await call(api.POST("/backup-destinations/{id}/reveal-password", { ...at(), body: { confirm: typed.trim() } }))).password;
    } catch (err) {
      error = describe(err);
    } finally {
      pending = false;
    }
  }

  async function confirm() {
    pending = true;
    error = "";
    try {
      await call(api.POST("/backup-destinations/{id}/confirm-password", at()));
      onDone(true);
    } catch (err) {
      error = describe(err);
    } finally {
      pending = false;
    }
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(password);
      copied = true;
    } catch {
      error = "Copying failed in this browser. Select the password and copy it by hand.";
    }
  }

  const fileHref = $derived(`data:text/plain;charset=utf-8,${encodeURIComponent(`restic repository password for "${destination.name}"\n${destination.restic?.repository ?? ""}\n\n${password}\n`)}`);
</script>

<Modal
  open={true}
  onOpenChange={(o) => !o && onDone(false)}
  title={password ? `Save the password of ${destination.name}` : `Show the password of ${destination.name}`}
  locked={!!generated}
>
  {#if password}
    <div class="grid gap-3">
      <p>Without this password nobody can read the backups in {destination.name}.</p>
      <p>Keep a copy off this host, for example in a password manager.</p>
      <output class="block rounded-md border border-line bg-sunken px-3 py-2 break-all select-all" aria-label="Repository password">{password}</output>
      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn" onclick={copy}>{copied ? "Copied" : "Copy"}</button>
        <a class="btn" href={fileHref} download="{destination.name}-restic-password.txt">Download</a>
      </div>
      {#if generated}
        <p class="text-xs text-muted">It can be shown again from the destination's menu.</p>
      {/if}
      {#if error}<p role="alert" class="text-bad">{error}</p>{/if}
    </div>
  {:else}
    <form id="{id}-reveal" class="grid gap-1.5" onsubmit={reveal}>
      <label for="{id}-name" class="text-muted">Type {destination.name} to show the password</label>
      <input id="{id}-name" class="input" autocomplete="off" bind:value={typed} />
      {#if error}<p role="alert" class="text-bad">{error}</p>{/if}
    </form>
  {/if}
  {#snippet actions()}
    {#if !password}
      <button type="submit" form="{id}-reveal" class="btn btn-primary" disabled={pending || typed.trim() !== destination.name}>Show password</button>
    {:else if mustConfirm}
      <button type="button" class="btn btn-primary" disabled={pending} onclick={confirm}>I have saved it</button>
    {/if}
  {/snippet}
</Modal>
