<script lang="ts">
  import { CircleArrowUp, ShieldAlert } from "@lucide/svelte";
  import type { Stack } from "#lib/api/client.ts";

  interface Props {
    stack: Pick<Stack, "update_state" | "generation">;
  }
  let { stack }: Props = $props();
</script>

<!-- What a stack has waiting, on its card and its page. The one status that
     sits on its soft tint. -->
{#if stack.generation > 0 && stack.update_state === "needs_approval"}
  <span class="inline-flex items-center gap-1 rounded-full bg-warn-soft py-0.5 pr-2.5 pl-1.5 text-xs whitespace-nowrap text-warn">
    <ShieldAlert class="size-3.5" aria-hidden="true" />needs approval
  </span>
{:else if stack.generation > 0 && stack.update_state === "commits_available"}
  <span class="inline-flex items-center gap-1 rounded-full bg-accent-soft py-0.5 pr-2.5 pl-1.5 text-xs whitespace-nowrap text-accent">
    <CircleArrowUp class="size-3.5" aria-hidden="true" />update ready
  </span>
{/if}
