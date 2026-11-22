<script lang="ts" module>
  export type Tone = "ok" | "warn" | "bad" | "info" | "idle";
</script>

<script lang="ts">
  import type { Snippet } from "svelte";
  import StatusMark from "./StatusMark.svelte";

  interface Props {
    tone: Tone;
    children: Snippet;
  }
  let { tone, children }: Props = $props();
</script>

<!-- Status as Unraid draws it: one coloured mark whose shape says the state
     (circle, triangle, cross, diamond, ring), then one lowercase word in the
     text colour. No background (DESIGN.md, Status). -->
<span class="inline-flex items-center gap-1.5 text-sm font-normal whitespace-nowrap lowercase">
  <StatusMark {tone} />
  {@render children()}
</span>
