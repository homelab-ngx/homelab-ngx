<script lang="ts">
  import { createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import { bytes } from "#lib/format.ts";
  import { diskSegments, HOLDS_WORD, installAddress, installProgress, largestFree, planSegments, STAGES, type Segment } from "#lib/installer.ts";
  import Disclosure from "#lib/components/Disclosure.svelte";
  import Help from "#lib/components/Help.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import DiskMap from "./DiskMap.svelte";
  import Ipv4Fields from "#lib/components/host/Ipv4Fields.svelte";
  import { untrack } from "svelte";

  type InstallerState = Schema<"InstallerState">;
  type Plan = Schema<"InstallPlan">;

  interface Props {
    initial: InstallerState;
  }
  let { initial }: Props = $props();

  const id = $props.id();
  const queryClient = useQueryClient();
  const KEY = ["daemon", "installer"] as const;
  // One browser holds the installer at a time: the first to open it, until
  // its page has been closed for ten minutes or another takes it over with
  // the code on the machine's screen. The token is kept for this browser,
  // so a closed tab comes back to its install.
  const TOKEN = "homelab-installer-token";
  function stored(): string {
    try {
      return localStorage.getItem(TOKEN) ?? "";
    } catch {
      return "";
    }
  }
  let token = $state(stored());
  function hold(t: string) {
    token = t;
    try {
      localStorage.setItem(TOKEN, t);
    } catch {
      /* the page still works for this load */
    }
    void queryClient.invalidateQueries({ queryKey: KEY });
  }
  const headers = $derived({ "X-Installer-Token": token });

  const query = createQuery(() => ({
    queryKey: KEY,
    queryFn: () => call<InstallerState>(api.GET("/installer", { headers: token ? { "X-Installer-Token": token } : {} })),
    initialData: initial,
    refetchInterval: (q) => (q.state.data?.step === "installing" ? 1_500 : 10_000),
  }));
  const st = $derived(query.data ?? initial);
  // A token the installer does not know (one from before it restarted)
  // holds nothing.
  const mine = $derived(st.mine);
  // With a token kept from before, the first answer that carried it says
  // whose the installer is; until then nothing is offered.
  const settled = $derived(!token || query.isFetchedAfterMount);

  let claimError = $state("");
  let claiming = false;
  $effect(() => {
    if (st.mine || st.claimed || claiming) return;
    claiming = true;
    call(api.POST("/installer/claim"))
      .then((r) => hold(r.token))
      .catch((e: unknown) => (claimError = describe(e)))
      .finally(() => (claiming = false));
  });

  let code = $state("");
  function takeOver(e: SubmitEvent) {
    e.preventDefault();
    claimError = "";
    call(api.POST("/installer/takeover", { body: { code } }))
      .then((r) => {
        code = "";
        hold(r.token);
      })
      .catch((e: unknown) => (claimError = describe(e)));
  }

  // One disk at a time: the first one an install can go on, until another
  // is chosen; the segment selected on it, its largest free run at first.
  let chosen = $state("");
  const disk = $derived(st.disks.find((d) => d.name === chosen) ?? st.disks.find((d) => !d.in_use && d.erase.ok) ?? st.disks[0]);
  const segs = $derived(disk ? diskSegments(disk) : []);
  let selectedKey = $state<string | null>(null);
  const selected = $derived(segs.find((s) => s.key === selectedKey) ?? largestFree(segs) ?? null);
  // A system's row is two partitions (its root and the verity one): Delete takes both.
  const canDelete = $derived(!!selected && selected.members.length > 0 && !disk?.in_use);
  const canHere = $derived(selected?.holds === "free" && !!disk?.beside.ok);

  // The plan for the disk and mode picked; the host's name and time zone.
  let pick = $state<{ disk: string; erase: boolean; region?: number } | null>(null);
  let plan = $state<Plan | null>(null);
  let hostname = $state("homelab");
  let timezone = $state(browserZone());
  let sshKeys = $state("");
  // DHCP at first; switching to static starts from the medium's own lease.
  let ipv4 = $state(untrack(() => ({ ...initial.current, dns: initial.current.dns ?? [], mode: "dhcp" as const })));
  // After the plan: the installed system's name and zone, then its network.
  let page = $state<"plan" | "system" | "network">("plan");
  const zones = zoneList();
  let error = $state("");
  let pending = $state(false);
  const planned = $derived(disk && plan?.ok ? planSegments(disk, plan.rows) : []);

  /** The IANA zones the browser knows, with UTC and its own zone always in. */
  function zoneList(): string[] {
    let all: string[] = [];
    try {
      all = Intl.supportedValuesOf("timeZone");
    } catch {
      /* an older browser: its own zone and UTC */
    }
    return [...new Set([...all, browserZone(), "UTC"])].sort();
  }

  function browserZone(): string {
    try {
      return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    } catch {
      return "UTC";
    }
  }

  function showDisk(name: string) {
    chosen = name;
    selectedKey = null;
  }

  // The boot loader's EFI partition: this disk's (new or kept), or one on
  // another disk (Windows'), so one boot menu lists both systems. One
  // smaller than the room the kernels need is not offered; the plan says
  // when a larger one is too full.
  const ESP_ROOM = 400 * 2 ** 20;
  let esp = $state("");
  const espChoices = $derived(
    st.disks
      .filter((d) => !d.in_use && d.name !== pick?.disk)
      .flatMap((d) => d.partitions.filter((p) => p.holds === "esp" && p.size >= ESP_ROOM).map((p) => ({ name: p.name, model: d.model || d.name }))),
  );

  async function choose(erase: boolean) {
    if (!disk) return;
    // Into free space: the free region selected on the bar.
    pick = { disk: disk.name, erase, region: !erase && selected?.holds === "free" ? selected.start : undefined };
    const ownESP = !erase && disk.partitions.some((p) => p.holds === "esp");
    esp = ownESP ? "" : (espChoices[0]?.name ?? "");
    page = "plan";
    await replan();
  }

  async function replan() {
    if (!pick) return;
    plan = null;
    error = "";
    try {
      plan = await call(api.POST("/installer/plan", { body: { disk: pick.disk, erase: pick.erase, esp: esp || undefined, region: pick.region } }));
    } catch (e) {
      error = describe(e);
    }
  }

  // Erasing asks for the disk's name; deleting a partition for its name.
  // name is what is typed to confirm; parts are the partitions a delete removes.
  let confirm = $state<{ kind: "erase" | "delete"; name: string; parts?: string[] } | null>(null);
  let typed = $state("");

  async function act(fn: () => Promise<unknown>) {
    pending = true;
    error = "";
    try {
      await fn();
      await queryClient.invalidateQueries({ queryKey: KEY });
    } catch (e) {
      error = describe(e);
    } finally {
      pending = false;
    }
  }

  const install = () =>
    act(() => call(api.POST("/installer/install", { headers, body: { disk: pick!.disk, erase: pick!.erase, esp: esp || undefined, region: pick!.region, hostname, timezone, ipv4, ssh_keys: sshKeys
            .split("\n")
            .map((k) => k.trim())
            .filter(Boolean) } })));

  function confirmed(e: SubmitEvent) {
    e.preventDefault();
    const c = confirm;
    if (!c || typed !== c.name || !disk) return;
    confirm = null;
    typed = "";
    if (c.kind === "erase") void install();
    else
      void act(async () => {
        for (const part of c.parts ?? [c.name]) {
          await call(api.DELETE("/installer/disks/{disk}/partitions/{part}", { headers, params: { path: { disk: disk.name, part } }, body: { confirm: part } }));
        }
        selectedKey = null;
      });
  }

  const STEPS = ["Disk", "Plan", "System", "Network", "Install"];
  const step = $derived(
    st.step === "installing" || st.step === "installed" ? 4 : page === "network" ? 3 : page === "system" ? 2 : pick ? 1 : 0,
  );
  const progress = $derived(installProgress(st.step, st.phase, st.log));

  /** Keeps a log scrolled to its newest line as lines come in. */
  function follow(lines: readonly string[]) {
    return (node: HTMLElement) => {
      void lines.length;
      node.scrollTop = node.scrollHeight;
    };
  }
</script>

<main class="mx-auto grid max-w-5xl gap-4 px-4 py-8">
  <header class="flex flex-wrap items-baseline gap-x-4 gap-y-2">
    <span class="text-muted">Homelab-ngx</span>
    <h1 class="flex flex-wrap items-center gap-2 text-xl font-semibold">
      <Help label="Install">
        <p>The installer writes the system onto a disk of this machine: erased, or into free space next to another system such as Windows.</p>
        <p class="mt-2">It needs 64 GB. Partitions are only removed when deleted here. To make room beside Windows, shrink its volume in Windows Disk Management first.</p>
      </Help>
    </h1>
    <ol class="ml-auto flex gap-1 text-sm">
      {#each STEPS as s, i (s)}
        <li class="rounded-full px-2.5 py-0.5 {i === step ? 'bg-accent-soft font-medium text-accent' : i < step ? 'text-ok' : 'text-muted'}">{i < step ? "✓ " : ""}{s}</li>
      {/each}
    </ol>
  </header>

  {#if mine && st.step !== "installing" && st.step !== "installed"}
    <dl class="flex flex-wrap gap-x-6 gap-y-1">
      <div class="flex gap-2">
        <dt class="text-muted">Memory</dt>
        <dd>{bytes(st.machine.memory)}</dd>
      </div>
      <div class="flex flex-wrap gap-2">
        <dt class="text-muted"><Help label="Secure Boot">The system's kernel is not signed: the installed system starts only with Secure Boot off in the firmware.</Help></dt>
        <dd>{#if st.machine.secure_boot}<Pill tone="warn">on</Pill>{:else}off{/if}</dd>
      </div>
      <div class="flex flex-wrap gap-2">
        <dt class="text-muted"><Help label="Boot entries">Whether the firmware lets the install register the new system. Where it does not, pick the disk in the firmware's boot menu after the install.</Help></dt>
        <dd>{#if st.machine.boot_entries}writable{:else}<Pill tone="warn">read-only</Pill>{/if}</dd>
      </div>
    </dl>
  {/if}
  {#if !mine && st.claimed && settled}
    <form class="card grid gap-3" onsubmit={takeOver}>
      <p>The installer is open in another browser.</p>
      <div class="flex flex-wrap items-end gap-2">
        <div class="grid gap-1">
          <label for="{id}-code" class="text-muted">
            <Help label="Code">The six digits on this machine's own screen, under the installer's address. They move the installer to this browser.</Help>
          </label>
          <input id="{id}-code" class="input w-32" inputmode="numeric" autocomplete="off" maxlength="6" bind:value={code} />
        </div>
        <button type="submit" class="btn btn-primary" disabled={code.trim().length !== 6}>Take over</button>
      </div>
      {#if claimError}<p role="alert" class="text-bad">{claimError}</p>{/if}
    </form>
  {:else if !mine}
    {#if claimError}<p role="alert" class="card text-bad">{claimError}</p>{/if}
  {:else if claimError}
    <p role="alert" class="card text-bad">{claimError}</p>
  {:else if st.step === "installing" || st.step === "installed"}
    <section class="card grid gap-3" aria-labelledby="{id}-run">
      <h2 id="{id}-run" class="flex items-center gap-2 font-semibold">
        <span class="inline-block size-2 rounded-full {st.step === 'installed' ? 'bg-ok' : 'animate-pulse bg-accent'}"></span>
        {st.step === "installed" ? "Installed on" : "Installing on"}
        {st.disk}
      </h2>
      <div class="grid gap-1.5">
        <div class="h-2 overflow-hidden rounded-full bg-sunken" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={progress.percent} aria-label="Install progress">
          <div class="h-full rounded-full {st.step === 'installed' ? 'bg-ok' : 'bg-accent'} transition-[width] duration-700" style="width: {progress.percent}%"></div>
        </div>
        <p class="text-muted">
          {st.step === "installed" ? "Done" : `Step ${progress.stage} of ${STAGES.length} · ${progress.label}…`}
        </p>
      </div>
      <Disclosure label="Details">
        <div class="grid gap-3">
          <ol class="grid gap-1">
            {#each STAGES as s, i (s.label)}
              <li class="flex items-center gap-2 {i + 1 > progress.stage ? 'text-muted' : ''}">
                <span class="w-3 font-semibold text-ok">{i + 1 < progress.stage ? "✓" : i + 1 === progress.stage ? "•" : ""}</span>{s.label}
              </li>
            {/each}
          </ol>
          <pre class="h-96 overflow-auto rounded-md bg-sunken p-3 text-xs" {@attach follow(st.log)}>{st.log.join("\n")}</pre>
        </div>
      </Disclosure>
      {#if st.step === "installed"}
        {@const ip = installAddress(st)}
        <!-- Without a firmware boot entry for the new system there is no
             restart into it: the machine is turned off and started by hand. -->
        <ol class="grid list-decimal gap-1 pl-5">
          {#if st.can_restart}
            <li>Restart</li>
            <li>
              Open <a class="link" href="http://{st.hostname}.local">http://{st.hostname}.local</a>{#if ip} or <a class="link" href="http://{ip}">http://{ip}</a>{/if}
            </li>
            <li>Remove the installer medium</li>
          {:else}
            <li>Turn off</li>
            <li>Remove the installer medium</li>
            <li>
              Turn on, then open <a class="link" href="http://{st.hostname}.local">http://{st.hostname}.local</a>{#if ip} or <a class="link" href="http://{ip}">http://{ip}</a>{/if}
            </li>
          {/if}
        </ol>
        <div class="flex flex-wrap items-center gap-2">
          {#if st.can_restart}
            <button type="button" class="btn btn-primary" disabled={pending} onclick={() => act(() => call(api.POST("/installer/restart", { headers })))}>Restart</button>
          {/if}
          <button type="button" class="btn {st.can_restart ? '' : 'btn-primary'}" disabled={pending} onclick={() => act(() => call(api.POST("/installer/poweroff", { headers })))}>Turn off</button>
        </div>
        <!-- On an EFI partition shared with another system the fallback
             loader stays that system's: firmware that ignores its boot
             entries starts it, and firmware that took none has no entry
             for this one. -->
        {#if st.shared_esp && st.can_restart}
          <p class="text-xs text-muted">If the other system starts instead, pick Linux Boot Manager in the firmware's boot menu.</p>
        {:else if st.shared_esp}
          <p class="text-xs text-muted">The firmware took no boot entry. Add one in its setup for <code>\EFI\systemd\systemd-bootx64.efi</code>.</p>
        {:else if st.can_restart}
          <p class="text-xs text-muted">After Turn off, remove the installer medium before turning on.</p>
        {/if}
      {/if}
    </section>
  {:else if pick && disk && page === "system"}
    <section class="card grid gap-0 overflow-hidden p-0" aria-labelledby="{id}-system">
      <div class="flex flex-wrap items-center gap-2 border-b border-line p-3">
        <h2 id="{id}-system" class="flex-1 font-semibold">System</h2>
        <button type="button" class="btn" onclick={() => (page = "plan")}>Back</button>
      </div>
      <div class="grid gap-4 p-3 sm:grid-cols-2">
        <div class="grid gap-1">
          <label for="{id}-host" class="flex flex-wrap items-center gap-1 text-muted">
            <Help label="Host name">The installed system answers at this name followed by .local on the network.</Help>
          </label>
          <input id="{id}-host" class="input" autocomplete="off" bind:value={hostname} />
        </div>
        <div class="grid gap-1">
          <label for="{id}-tz" class="flex flex-wrap items-center gap-1 text-muted">
            <Help label="Time zone">Preset from this browser. Logs and schedules use it; it can be changed later in Settings.</Help>
          </label>
          <select id="{id}-tz" class="input" bind:value={timezone}>
            {#each zones as z (z)}
              <option value={z}>{z.replaceAll("_", " ")}</option>
            {/each}
          </select>
        </div>
        <div class="grid gap-1 sm:col-span-2">
          <label for="{id}-ssh" class="flex flex-wrap items-center gap-1 text-muted">
            <Help label="SSH keys">Optional. Public keys that may log in as root over SSH, one per line; they can be changed later in Settings. Without one, only the host's own screen gives a shell.</Help>
          </label>
          <textarea id="{id}-ssh" class="input min-h-20" autocomplete="off" spellcheck="false" placeholder="ssh-ed25519 AAAA… user@laptop" bind:value={sshKeys}></textarea>
        </div>
      </div>
      <div class="flex justify-end border-t border-line p-3">
        <button type="button" class="btn btn-primary" disabled={!hostname} onclick={() => (page = "network")}>Next</button>
      </div>
    </section>
  {:else if pick && disk && page === "network"}
    <section class="card grid gap-0 overflow-hidden p-0" aria-labelledby="{id}-network">
      <div class="flex flex-wrap items-center gap-2 border-b border-line p-3">
        <h2 id="{id}-network" class="flex flex-1 flex-wrap items-center gap-1 font-semibold">
          <Help label="Network">DHCP takes an address from the router. A static address fits a router without reservations; it can be changed later in Settings.</Help>
        </h2>
        <button type="button" class="btn" onclick={() => (page = "system")}>Back</button>
      </div>
      <div class="p-3">
        <Ipv4Fields bind:value={ipv4} />
      </div>
      <div class="flex flex-wrap items-center gap-3 border-t border-line p-3">
        <button
          type="button"
          class="btn {pick.erase ? 'btn-danger' : 'btn-primary'}"
          disabled={pending || !hostname}
          onclick={() => (pick!.erase ? ((confirm = { kind: "erase", name: pick!.disk }), (typed = "")) : install())}>Install</button>
        <span class="text-muted">{pick.erase ? "Erases" : "Free space on"} {pick.disk} · opens at http://{hostname || "…"}.local</span>
      </div>
    </section>
  {:else if pick && disk}
    <section class="card grid gap-0 overflow-hidden p-0" aria-labelledby="{id}-plan">
      <div class="flex flex-wrap items-center gap-2 border-b border-line p-3">
        <h2 id="{id}-plan" class="flex-1 font-semibold">{pick.erase ? "Erase" : "Free space on"} {pick.disk}</h2>
        <button type="button" class="btn" onclick={() => ((pick = null), (plan = null))}>Back</button>
      </div>
      {#if espChoices.length}
        <fieldset class="grid gap-2 border-b border-line p-3">
          <legend class="sr-only">Boot partition</legend>
          <span class="flex flex-wrap items-center gap-1 text-muted">
            <Help label="Boot partition">The EFI partition the boot loader and kernel go on. Sharing another disk's, such as Windows', puts both systems in one boot menu.</Help>
          </span>
          <div class="flex flex-wrap gap-x-5 gap-y-2">
            <label class="flex items-center gap-2"><input type="radio" name="{id}-esp" value="" bind:group={esp} onchange={replan} /> {pick.disk}</label>
            {#each espChoices as c (c.name)}
              <label class="flex items-center gap-2"><input type="radio" name="{id}-esp" value={c.name} bind:group={esp} onchange={replan} /> {c.name} · {c.model}</label>
            {/each}
          </div>
        </fieldset>
      {/if}
      {#if plan?.ok}
        <div class="grid gap-2 p-3">
          <span class="text-xs text-muted">Now</span>
          <DiskMap segments={segs} size={disk.size} label="{disk.name} now" />
          <span class="text-xs text-muted">After</span>
          <DiskMap segments={planned} size={disk.size} label="{disk.name} after the install" />
        </div>
        <div class="overflow-x-auto">
          <table class="w-full">
            <thead>
              <tr class="text-left text-xs text-muted">
                <th class="px-3 py-2 font-medium">Partition</th>
                <th class="px-3 py-2 font-medium">Holds</th>
                <th class="px-3 py-2 text-right font-medium">Size</th>
                <th class="px-3 py-2 font-medium">Change</th>
              </tr>
            </thead>
            <tbody>
              {#each planned as s (s.key)}
                <tr class="border-t border-line">
                  <td class="px-3 py-2 whitespace-nowrap">{s.names || "–"}</td>
                  <td class="px-3 py-2">{s.title}</td>
                  <td class="px-3 py-2 text-right whitespace-nowrap">{bytes(s.size)}</td>
                  <td class="px-3 py-2 {s.change === 'new' ? 'text-ok' : 'text-muted'}">{s.holds === "free" ? "free" : s.change}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        <div class="flex justify-end border-t border-line p-3">
          <button type="button" class="btn btn-primary" onclick={() => (page = "system")}>Next</button>
        </div>
      {:else if plan}
        <p class="p-3 text-bad">{plan.reason}</p>
      {:else if !error}
        <p class="p-3 text-muted">Planning…</p>
      {/if}
    </section>
  {:else if disk}
    {#if st.step === "failed"}
      <section role="alert" class="card grid gap-2">
        <p class="text-bad">{st.error}</p>
        <pre class="max-h-60 overflow-auto rounded-md bg-sunken p-3 text-xs">{st.log.join("\n")}</pre>
      </section>
    {/if}
    <section class="card grid gap-0 overflow-hidden p-0" aria-label="Disk">
      <div class="flex flex-wrap items-center gap-2 border-b border-line p-3">
        <select class="input max-w-md min-w-0 flex-1" aria-label="Disk" value={disk.name} onchange={(e) => showDisk(e.currentTarget.value)}>
          {#each st.disks as d (d.name)}
            <option value={d.name} disabled={d.in_use}>{d.name} · {d.model || "disk"} · {bytes(d.size)}{d.in_use ? " · installer medium" : ""}</option>
          {/each}
        </select>
        <span class="flex-1"></span>
        <button type="button" class="btn" disabled={!canDelete || pending} onclick={() => ((confirm = { kind: "delete", name: selected!.members[0]!, parts: selected!.members }), (typed = ""))}>Delete</button>
        <button type="button" class="btn" disabled={!disk.erase.ok || pending} title={disk.erase.reason} onclick={() => choose(true)}>Erase disk</button>
        <button type="button" class="btn btn-primary" disabled={!canHere || pending} onclick={() => choose(false)}>Install here</button>
      </div>
      <div class="grid gap-1.5 p-3">
        <div class="flex justify-between text-xs text-muted">
          <span>{disk.model || "disk"}{disk.bus ? ` · ${disk.bus}` : ""} · {disk.table === "none" ? "blank" : disk.table.toUpperCase()}</span>
          <span>{bytes(disk.size)}</span>
        </div>
        <DiskMap segments={segs} size={disk.size} label="Partitions of {disk.name}" selected={selected?.key} onSelect={(s: Segment) => (selectedKey = s.key)} />
      </div>
      <div class="overflow-x-auto">
        <table class="w-full">
          <thead>
            <tr class="text-left text-xs text-muted">
              <th class="px-3 py-2 font-medium">Partition</th>
              <th class="px-3 py-2 font-medium">Holds</th>
              <th class="px-3 py-2 font-medium">Name</th>
              <th class="px-3 py-2 font-medium">File system</th>
              <th class="px-3 py-2 text-right font-medium">Size</th>
            </tr>
          </thead>
          <tbody>
            {#each segs as s (s.key)}
              <tr class="cursor-pointer border-t border-line {selected?.key === s.key ? 'bg-accent-soft' : 'hover:bg-sunken'}" onclick={() => (selectedKey = s.key)}>
                <td class="px-3 py-2 whitespace-nowrap">
                  <button type="button" class="text-left" onclick={() => (selectedKey = s.key)}>{s.names || "unallocated"}</button>
                </td>
                <td class="px-3 py-2">{HOLDS_WORD[s.holds]}</td>
                <td class="px-3 py-2">{s.holds === "free" ? "" : s.title}</td>
                <td class="px-3 py-2 text-muted">{s.fs}</td>
                <td class="px-3 py-2 text-right whitespace-nowrap">{bytes(s.size)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if selected}
        <p class="border-t border-line p-3 text-muted">
          {#if selected.holds === "free"}
            {disk.beside.ok ? `${bytes(selected.size)} free` : disk.beside.reason}
          {:else if selected.holds === "windows"}
            To make room, shrink this volume in Windows Disk Management.
          {:else}
            {selected.names} · {selected.title} · {bytes(selected.size)}
          {/if}
        </p>
      {/if}
    </section>
  {/if}
  {#if error}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{error}</p>{/if}
</main>

{#if confirm}
  <Modal
    open={true}
    onOpenChange={(o) => !o && (confirm = null)}
    title={confirm.kind === "erase" ? `Erase ${confirm.name}` : `Delete ${(confirm.parts ?? [confirm.name]).join(" and ")}`}
    description={confirm.kind === "erase"
      ? "Everything on the disk is removed."
      : (confirm.parts?.length ?? 1) > 1
        ? "Both partitions and everything on them are removed."
        : "The partition and everything on it are removed."}
  >
    <form id="{id}-confirm" class="grid gap-1" onsubmit={confirmed}>
      <label for="{id}-typed" class="text-muted">Type {confirm.name}</label>
      <input id="{id}-typed" class="input" autocomplete="off" bind:value={typed} />
    </form>
    {#snippet actions()}
      <button type="submit" form="{id}-confirm" class="btn btn-danger" disabled={typed !== confirm?.name}>{confirm?.kind === "erase" ? "Erase and install" : "Delete"}</button>
    {/snippet}
  </Modal>
{/if}
