<script lang="ts">
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { movedTile } from "#lib/settings.ts";

  // Network's cards each have a page of their own now; an old link's anchor names the card.
  onMount(() => void goto(movedTile(page.url.hash), { replaceState: true }));
</script>
