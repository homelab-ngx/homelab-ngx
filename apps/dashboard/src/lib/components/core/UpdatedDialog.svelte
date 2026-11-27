<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { replaceState } from "$app/navigation";
  import { page } from "$app/state";
  import { api, call, type Schema } from "#lib/api/client.ts";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import {
    changelogKey,
    fetchChangelog,
    isReleaseVersion,
    SEEN_VERSION_KEY,
    upgradeNotes,
  } from "./changelog.ts";
  import ReleaseChanges from "./ReleaseChanges.svelte";
  import { compareVersions, runningVersion } from "./core.ts";

  function readSeenVersion(): string {
    try {
      return localStorage.getItem(SEEN_VERSION_KEY) ?? "";
    } catch {
      return "";
    }
  }
  function writeSeenVersion(v: string) {
    try {
      localStorage.setItem(SEEN_VERSION_KEY, v);
    } catch {
      // A private window keeps dismissal for this load only.
    }
  }

  // The power page ends an update here with the version it installed. When
  // absent, a newer daemon release than the last seen opens the same dialog.
  const want = page.url.searchParams.get("updated") ?? "";
  const fromParam = page.url.searchParams.get("from") ?? "";
  const baseline = want ? "" : readSeenVersion();
  let dismissed = $state(false);

  const image = createQuery(() => ({
    queryKey: ["daemon", "system-updates", "image"],
    queryFn: () => call(api.GET("/system-updates/image")),
    enabled: want !== "",
    retry: false,
  }));
  const version = createQuery(() => ({
    queryKey: ["daemon", "version"],
    queryFn: () => call<Schema<"Version">>(api.GET("/version")),
    enabled: want === "",
    staleTime: 60_000,
    retry: false,
  }));

  const daemonVersion = $derived(version.data?.version ?? "");
  $effect(() => {
    if (want || !isReleaseVersion(daemonVersion)) return;
    if (!baseline || compareVersions(daemonVersion, baseline) < 0) {
      writeSeenVersion(daemonVersion);
    }
  });

  const autoUpgrade = $derived(
    !want && isReleaseVersion(daemonVersion) && baseline !== "" && compareVersions(daemonVersion, baseline) > 0
      ? { from: baseline, to: daemonVersion }
      : null,
  );
  const open = $derived(!dismissed && (want !== "" || autoUpgrade !== null));

  const running = $derived(image.data ? runningVersion(image.data) : (autoUpgrade?.to ?? ""));
  const targetVersion = $derived(running || want);
  const wentBack = $derived(want !== "" && running !== "" && compareVersions(running, want) < 0);
  const fromVersion = $derived(
    fromParam ||
      autoUpgrade?.from ||
      (image.data?.installed.find((v) => v.state === "good" && compareVersions(v.version, targetVersion) < 0)?.version ?? ""),
  );

  const changelog = createQuery(() => ({
    queryKey: changelogKey,
    queryFn: fetchChangelog,
    enabled: open && !wentBack,
    staleTime: 5 * 60_000,
    retry: false,
  }));
  const notes = $derived(wentBack || !targetVersion ? [] : upgradeNotes(changelog.data?.releases ?? [], targetVersion, fromVersion));

  function close() {
    dismissed = true;
    if (!wentBack && targetVersion && isReleaseVersion(targetVersion)) {
      writeSeenVersion(targetVersion);
    }
    const u = new URL(page.url.href);
    if (u.searchParams.has("updated") || u.searchParams.has("from")) {
      u.searchParams.delete("updated");
      u.searchParams.delete("from");
      replaceState(u, {});
    }
  }
</script>

<Modal {open} onOpenChange={(o) => !o && close()} title={wentBack ? "Update went back" : "Updated"}>
  {#snippet children()}
    <div class="grid gap-3">
      <p class="flex flex-wrap items-center gap-x-2">
        <Pill tone={wentBack ? "warn" : "ok"}>running</Pill>
        <span class="tabular-nums">{targetVersion}</span>
        {#if !wentBack && fromVersion && compareVersions(targetVersion, fromVersion) > 0}
          <span class="text-xs text-muted tabular-nums">from {fromVersion}</span>
        {/if}
      </p>
      {#if wentBack}
        <p class="text-muted">{want} did not start.</p>
      {:else if notes.length > 0}
        <div class="grid gap-3 border-t border-line pt-3">
          {#if notes.length === 1}
            {@const n = notes[0]!}
            <div class="flex items-center justify-between gap-2">
              <h3 class="font-semibold">What changed</h3>
              {#if n.date}<span class="text-xs text-muted tabular-nums">{n.date}</span>{/if}
            </div>
            <ReleaseChanges note={n} />
          {:else}
            {#each notes as n (n.version)}
              <div class="grid gap-1.5 border-t border-line pt-2 first:border-t-0 first:pt-0">
                <div class="flex flex-wrap items-center gap-x-2">
                  <h3 class="font-semibold tabular-nums">{n.version}</h3>
                  {#if n.date}<span class="text-xs text-muted tabular-nums">{n.date}</span>{/if}
                </div>
                <ReleaseChanges note={n} />
              </div>
            {/each}
          {/if}
        </div>
      {:else if changelog.isPending}
        <p class="text-xs text-muted">Loading changes…</p>
      {/if}
    </div>
  {/snippet}
</Modal>
