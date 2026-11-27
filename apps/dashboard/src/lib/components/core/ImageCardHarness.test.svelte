<script lang="ts">
  import { QueryClientProvider, type QueryClient } from "@tanstack/svelte-query";
  import type { CoreDeploy, ImageStatus } from "./core.ts";
  import ImageCard from "./ImageCard.svelte";

  let { client, image, onStarted }: { client: QueryClient; image: ImageStatus; onStarted?: (d: CoreDeploy) => void } = $props();
</script>

<QueryClientProvider {client}>
  <ImageCard {image} busy={false} onReboot={() => {}} {onStarted} />
</QueryClientProvider>
