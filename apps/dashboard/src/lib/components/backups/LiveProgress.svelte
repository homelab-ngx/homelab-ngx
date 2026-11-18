<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { api, call } from "#lib/api/client.ts";
  import { backupKeys, type RunDetail } from "#lib/backups.ts";
  import RunProgress from "./RunProgress.svelte";

  interface Props {
    /** The run in flight. */
    runId: number;
  }
  let { runId }: Props = $props();

  const run = createQuery(() => ({
    queryKey: backupKeys.run(runId),
    queryFn: () => call(api.GET("/backup-runs/{run}", { params: { path: { run: runId } } })) as Promise<RunDetail>,
    refetchInterval: 2_500,
  }));
</script>

<RunProgress run={run.data} compact />
