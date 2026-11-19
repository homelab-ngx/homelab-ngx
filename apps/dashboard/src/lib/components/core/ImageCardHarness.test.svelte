<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { ImageStatus } from "./core.ts";
  import ImageCard from "./ImageCard.svelte";

  let { client, image }: { client: QueryClient; image: ImageStatus } = $props();
</script>

<QueryClientProvider {client}>
  <ImageCard {image} busy={false} onReboot={() => {}} />
</QueryClientProvider>
