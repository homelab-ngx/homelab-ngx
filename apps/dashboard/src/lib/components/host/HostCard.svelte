<script lang="ts">
  import type { Snippet } from "svelte";
  import { describe } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";

  interface Props {
    /** The section's id, so System Settings' tiles can link to it. */
    anchor?: string;
    title: string;
    /** What the section is for, in a few words: the one hint it shows. */
    subtitle: string;
    /** The title's help. */
    help: string;
    /** The section's state, as a status mark and word beside the title. */
    status?: Snippet;
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
  let { anchor, title, subtitle, help, status, readOnly, dirty, problem = "", apply, reset, children }: Props = $props();

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

<!-- One section of the host's settings: its fields and one Apply. -->
<section id={anchor} class="card scroll-mt-16" aria-labelledby="{id}-h">
  <div class="flex flex-wrap items-start justify-between gap-x-3 gap-y-1">
    <div class="min-w-0">
      <h3 id="{id}-h" class="font-semibold"><Help label={title}>{help}</Help></h3>
      <p class="mt-0.5 text-xs text-muted">{subtitle}</p>
    </div>
    {#if status}<div class="pt-0.5">{@render status()}</div>{/if}
  </div>
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
