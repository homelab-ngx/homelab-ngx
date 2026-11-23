<script lang="ts">
  import {
    Bell,
    BrickWall,
    Clock,
    Cloud,
    Globe,
    House,
    Keyboard,
    KeyRound,
    Lock,
    LogOut,
    Network,
    Power,
    RefreshCw,
    RotateCcw,
    ScrollText,
    ShieldCheck,
    ShieldHalf,
    Sparkles,
    SunMoon,
    Waypoints,
  } from "@lucide/svelte";
  import { createQuery } from "@tanstack/svelte-query";
  import { api, call, describe } from "#lib/api/client.ts";
  import { fetchAssistant, keys } from "#lib/api/queries.ts";
  import { assistantLabel } from "#lib/assistant.ts";
  import { session } from "#lib/auth.svelte.ts";
  import { headline } from "#lib/components/core/core.ts";
  import PasswordForm from "#lib/components/PasswordForm.svelte";
  import PowerDialog from "#lib/components/PowerDialog.svelte";
  import StatusMark from "#lib/components/StatusMark.svelte";
  import { clockTime } from "#lib/format.ts";
  import { fetchHostSettings, fetchTailscale, hostKeys } from "#lib/host.ts";
  import { channelKeys, notificationKeys, type Channel, type Snapshot } from "#lib/notifications.ts";
  import { fetchPower, powerKey, type PowerAction } from "#lib/power.ts";
  import { fetchAudits, securityKeys } from "#lib/security.ts";
  import {
    appliedStatus,
    auditStatus,
    clockStatus,
    consoleLine,
    domainLine,
    firewallLine,
    hostLine,
    lanLine,
    notificationsStatus,
    sshLine,
    tailscaleStatus,
    tunnelStatus,
    vpnStatus,
    type TileStatus,
  } from "#lib/settings.ts";

  const host = createQuery(() => ({ queryKey: hostKeys.settings, queryFn: fetchHostSettings }));
  const s = $derived(host.data);
  const tailscale = createQuery(() => ({ queryKey: hostKeys.tailscale, queryFn: fetchTailscale, enabled: !!s?.tailscale.enabled }));
  const layouts = createQuery(() => ({
    queryKey: hostKeys.keyboardLayouts,
    queryFn: () => call(api.GET("/host-settings/keyboard-layouts")),
    staleTime: Infinity,
  }));

  // Only a host on the image answers this.
  const image = createQuery(() => ({
    queryKey: ["daemon", "system-updates", "image"],
    queryFn: () => call(api.GET("/system-updates/image")),
    retry: false,
  }));
  const updates = $derived.by((): TileStatus | undefined => {
    if (!image.data) return undefined;
    const h = headline(image.data);
    return { tone: h.tone, text: h.state };
  });

  const channels = createQuery(() => ({ queryKey: channelKeys.list, queryFn: () => call<Channel[]>(api.GET("/notification-channels")) }));
  const notes = createQuery(() => ({
    queryKey: notificationKeys.snapshot,
    queryFn: () => call<Snapshot>(api.GET("/notifications")),
    refetchInterval: 30_000,
  }));

  const assistant = createQuery(() => ({ queryKey: keys.assistant, queryFn: fetchAssistant }));
  const assistantStatus = $derived<TileStatus | undefined>(
    assistant.data && { tone: assistant.data.provider !== "none" ? "ok" : "idle", text: assistantLabel(assistant.data) },
  );
  const audits = createQuery(() => ({ queryKey: securityKeys.audits, queryFn: fetchAudits }));

  const power = createQuery(() => ({ queryKey: powerKey, queryFn: fetchPower }));
  const scheduled = $derived(power.data?.scheduled);
  const when = (a: PowerAction): TileStatus | undefined => (scheduled?.action === a ? { tone: "info", text: `at ${clockTime(scheduled.at)}` } : undefined);
  let powerAction = $state<PowerAction | null>(null);

  let changing = $state(false);
  let signOutError = $state("");
  async function signOut() {
    signOutError = "";
    try {
      await session.signOut();
    } catch (e) {
      signOutError = describe(e);
    }
  }

  type Theme = "system" | "light" | "dark";
  const read = (): Theme => {
    try {
      return (localStorage.getItem("theme") as Theme | null) ?? "system";
    } catch {
      return "system";
    }
  };
  let theme = $state<Theme>(read());

  function setTheme(t: Theme) {
    theme = t;
    try {
      localStorage.setItem("theme", t);
    } catch {
      // A private window: the choice lasts for this page only.
    }
    const dark = t === "dark" || (t === "system" && matchMedia("(prefers-color-scheme: dark)").matches);
    document.documentElement.classList.toggle("dark", dark);
  }

  type Icon = typeof House;

  const tileClass =
    "group flex min-h-24 flex-col items-center gap-1.5 rounded-lg px-2 pt-3.5 pb-3 text-center hover:bg-sunken focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent";
</script>

<svelte:head><title>System Settings · Homelab-ngx</title></svelte:head>

<h1 class="text-xl font-semibold">System Settings</h1>

{#snippet status(st: TileStatus | undefined)}
  <span class="flex max-w-full min-h-4 items-center justify-center gap-1.5 text-xs text-muted">
    {#if st?.tone}<StatusMark tone={st.tone} />{/if}
    {#if st}<span class="truncate">{st.text}</span>{/if}
  </span>
{/snippet}

{#snippet face(Icon: Icon, name: string, st: TileStatus | undefined, core: boolean)}
  <Icon class="size-7 {core ? 'text-core' : 'text-fg'}" strokeWidth={1.5} aria-hidden="true" />
  <span class="font-medium group-hover:underline {core ? 'text-core' : 'text-accent'}">{name}</span>
  {@render status(st)}
{/snippet}

{#snippet tile(Icon: Icon, name: string, href: string, st: TileStatus | undefined, core = false)}
  <a {href} class={tileClass}>{@render face(Icon, name, st, core)}</a>
{/snippet}

{#snippet action(Icon: Icon, name: string, onclick: () => void, st?: TileStatus)}
  <button type="button" class={tileClass} {onclick}>{@render face(Icon, name, st, false)}</button>
{/snippet}

{#snippet band(id: string, title: string, st?: TileStatus)}
  <h2 {id} class="flex items-center gap-3 border-b border-line bg-sunken px-3 py-2">
    <span class="label">{title}</span>
    {#if st}<span class="ml-auto">{@render status(st)}</span>{/if}
  </h2>
{/snippet}

<div class="mt-5 grid gap-5">
  <section class="overflow-hidden rounded-lg border border-line bg-surface" aria-labelledby="host-h">
    {@render band("host-h", "This host")}
    <div class="grid grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))] gap-1 p-3">
      {@render tile(House, "Host", "/settings/general#host", s && hostLine(s))}
      {@render tile(Clock, "Clock", "/settings/general#clock", s && clockStatus(s))}
      {@render tile(Keyboard, "Console", "/settings/general#console", s && consoleLine(s, layouts.data?.layouts))}
      {@render tile(RefreshCw, "Updates", "/settings/updates", updates, true)}
      {@render tile(Bell, "Notifications", "/settings/notifications", notificationsStatus(channels.data, notes.data))}
      {@render tile(ScrollText, "Logs", "/settings/logs", { text: "host, backups" })}
      {@render action(RotateCcw, "Reboot…", () => (powerAction = "reboot"), when("reboot"))}
      {@render action(Power, "Shut down…", () => (powerAction = "poweroff"), when("poweroff"))}
    </div>
  </section>

  <section class="overflow-hidden rounded-lg border border-line bg-surface" aria-labelledby="net-h">
    {@render band("net-h", "Network", s && appliedStatus(s))}
    <div class="grid grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))] gap-1 p-3">
      {@render tile(Globe, "Domain", "/settings/network#domain", s && domainLine(s))}
      {@render tile(Network, "LAN", "/settings/network#lan", s && lanLine(s))}
      {@render tile(Waypoints, "Tailscale", "/settings/network#tailscale", s && tailscaleStatus(s, tailscale.data))}
      {@render tile(ShieldHalf, "WireGuard VPN", "/settings/network#wireguard", s && vpnStatus(s))}
      {@render tile(Cloud, "Cloudflare Tunnel", "/settings/network#tunnel", s && tunnelStatus(s))}
      {@render tile(BrickWall, "Firewall", "/settings/network#firewall", s && firewallLine(s))}
      {@render tile(KeyRound, "SSH", "/settings/network#ssh", s && sshLine(s))}
    </div>
  </section>

  <section class="overflow-hidden rounded-lg border border-line bg-surface" aria-labelledby="ai-h">
    {@render band("ai-h", "Assistant")}
    <div class="grid grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))] gap-1 p-3">
      {@render tile(Sparkles, "AI assistant", "/settings/assistant", assistantStatus)}
      {@render tile(ShieldCheck, "Security audit", "/settings/security", auditStatus(audits.data))}
    </div>
  </section>

  <section class="overflow-hidden rounded-lg border border-line bg-surface" aria-labelledby="browser-h">
    {@render band("browser-h", "This browser")}
    <div class="grid grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))] gap-1 p-3">
      {@render action(Lock, "Change password…", () => (changing = true))}
      {@render action(LogOut, "Sign out", signOut)}
      <div class="flex min-h-24 flex-col items-center gap-1.5 px-2 pt-3.5 pb-3 text-center">
        <SunMoon class="size-7 text-fg" strokeWidth={1.5} aria-hidden="true" />
        <span id="theme-h" class="font-medium">Theme</span>
        <span class="flex overflow-hidden rounded-md border border-line text-xs" role="radiogroup" aria-labelledby="theme-h">
          {#each ["system", "light", "dark"] as const as t (t)}
            <button
              type="button"
              role="radio"
              aria-checked={theme === t}
              class="px-2 py-0.5 not-first:border-l not-first:border-line hover:bg-sunken focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent aria-checked:bg-accent-soft aria-checked:text-accent"
              onclick={() => setTheme(t)}
            >
              {t[0]!.toUpperCase() + t.slice(1)}
            </button>
          {/each}
        </span>
      </div>
    </div>
  </section>
  {#if signOutError}<p role="alert" class="rounded-md bg-bad-soft px-3 py-2 text-bad">{signOutError}</p>{/if}
</div>

{#if changing}<PasswordForm onClose={() => (changing = false)} />{/if}
{#if powerAction}<PowerDialog action={powerAction} onClose={() => (powerAction = null)} />{/if}
