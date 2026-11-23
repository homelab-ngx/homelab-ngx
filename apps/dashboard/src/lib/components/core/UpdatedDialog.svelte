<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { replaceState } from "$app/navigation";
  import { page } from "$app/state";
  import { api, call } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { compareVersions, runningVersion } from "./core.ts";

  // The power page ends an update here with the version it installed. The
  // version shown is the one the host runs now, read from the host.
  const want = page.url.searchParams.get("updated") ?? "";
  let open = $state(want !== "");

  const image = createQuery(() => ({
    queryKey: ["daemon", "system-updates", "image"],
    queryFn: () => call(api.GET("/system-updates/image")),
    enabled: want !== "",
    retry: false,
  }));
  const running = $derived(image.data ? runningVersion(image.data) : "");
  const wentBack = $derived(running !== "" && compareVersions(running, want) < 0);

  function close() {
    open = false;
    const u = new URL(page.url.href);
    u.searchParams.delete("updated");
    replaceState(u, {});
  }
</script>

<Modal open={open} onOpenChange={(o) => !o && close()} title={wentBack ? "Update went back" : "Updated"}>
  {#snippet children()}
    <div class="grid gap-2">
      <p class="flex flex-wrap items-center gap-x-2">
        <Pill tone={wentBack ? "warn" : "ok"}>running</Pill>
        <span class="tabular-nums">{running || want}</span>
      </p>
      {#if wentBack}<p class="text-muted">{want} did not start.</p>{/if}
    </div>
  {/snippet}
</Modal>
