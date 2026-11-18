<script lang="ts">
  import type { Snippet } from "svelte";
  import { describe } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";

  interface Props {
    title: string;
    /** The title's help. */
    help: string;
    /** Values from the NixOS configuration: shown, not changed. */
    readOnly: boolean;
    /** Something differs from what is stored. */
    dirty: boolean;
    /** Why the card cannot be applied yet, or "". */
    problem?: string;
    apply: () => Promise<unknown>;
    reset: () => void;
    children: Snippet;
  }
  let { title, help, readOnly, dirty, problem = "", apply, reset, children }: Props = $props();

  const id = $props.id();
  let pending = $state(false);
  let error = $state("");

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!dirty || problem) return;
    pending = true;
    error = "";
    try {
      await apply();
    } catch (err) {
      error = describe(err);
    } finally {
      pending = false;
    }
  }
</script>

<!-- One section of the host's settings: its fields and one Apply (DESIGN.md, Value List). -->
<section class="card" aria-labelledby="{id}-h">
  <h2 id="{id}-h" class="font-semibold"><Help label={title}>{help}</Help></h2>
  <form class="mt-3 grid gap-3" onsubmit={submit}>
    <fieldset class="grid gap-3" disabled={readOnly}>
      {@render children()}
    </fieldset>
    {#if error}
      <p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>
    {:else if dirty && problem}
      <p class="text-muted">{problem}</p>
    {/if}
    {#if !readOnly}
      <div class="flex justify-end gap-2">
        <button type="button" class="btn" disabled={!dirty || pending} onclick={reset}>Reset</button>
        <button type="submit" class="btn btn-primary" disabled={!dirty || !!problem || pending} title={problem}>{pending ? "Applying…" : "Apply"}</button>
      </div>
    {/if}
  </form>
</section>
