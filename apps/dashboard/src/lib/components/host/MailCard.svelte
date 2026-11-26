<script lang="ts">
  import { untrack } from "svelte";
  import { api, call } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import Pill from "#lib/components/Pill.svelte";
  import { changed, type HostSettings } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  type TLSMode = "starttls" | "tls" | "none";

  let { settings, readOnly, onSaved }: Props = $props();
  const id = $props.id();

  const TLS_OPTIONS: { value: TLSMode; label: string }[] = [
    { value: "starttls", label: "STARTTLS (port 587)" },
    { value: "tls", label: "Implicit TLS (port 465)" },
    { value: "none", label: "Plaintext (no TLS)" },
  ];

  const stored = () => {
    const m = settings.smtp;
    const tls: TLSMode = m.tls === "tls" || m.tls === "none" ? m.tls : "starttls";
    return {
      enabled: m.enabled,
      host: m.host ?? "",
      port: m.port || (tls === "tls" ? 465 : 587),
      tls,
      username: m.username ?? "",
      password: "",
    };
  };
  let draft = $state(untrack(stored));

  const userTrim = $derived(draft.username.trim());
  const needPassword = $derived(
    userTrim !== "" && (!settings.smtp.password_set || userTrim !== (settings.smtp.username ?? "")),
  );
  const problem = $derived.by(() => {
    if (!draft.enabled) return "";
    if (!draft.host.trim()) return "Enter an upstream SMTP host.";
    const p = Number(draft.port);
    if (!Number.isInteger(p) || p < 1 || p > 65535) return "Port must be between 1 and 65535.";
    if (userTrim !== "" && draft.tls === "none") return "Authentication needs STARTTLS or implicit TLS.";
    if (needPassword && draft.password === "") return "Enter the SMTP password.";
    return "";
  });

  function onSecurityChange() {
    if (draft.tls === "tls" && Number(draft.port) === 587) draft.port = 465;
    else if (draft.tls === "starttls" && Number(draft.port) === 465) draft.port = 587;
  }

  async function apply() {
    const body = {
      enabled: draft.enabled,
      host: draft.host.trim(),
      port: Number(draft.port),
      tls: draft.tls,
      username: userTrim,
      password: draft.password !== "" ? draft.password : undefined,
    };
    onSaved(await call(api.PUT("/host-settings/smtp", { body })));
  }
</script>

<HostCard
  subtitle="Relays outbound mail for the host and stacks on port 25."
  {readOnly}
  dirty={changed(draft, stored())}
  {problem}
  {apply}
  reset={() => (draft = stored())}
>
  {#snippet status()}
    {#if settings.smtp.enabled && settings.smtp.host}<Pill tone="ok">on</Pill>
    {:else}<Pill tone="idle">off</Pill>{/if}
  {/snippet}
  <label class="flex items-center gap-2"><input type="checkbox" class="accent-accent" bind:checked={draft.enabled} /> On</label>
  <div class="grid gap-3 sm:grid-cols-2">
    <div class="grid gap-1">
      <label for="{id}-host" class="text-muted">Host</label>
      <input id="{id}-host" class="input" autocomplete="off" placeholder="smtp.example.com" bind:value={draft.host} />
    </div>
    <div class="grid gap-1">
      <label for="{id}-port" class="text-muted">Port</label>
      <input id="{id}-port" class="input" type="number" min="1" max="65535" bind:value={draft.port} />
    </div>
    <div class="grid gap-1 sm:col-span-2">
      <label for="{id}-tls" class="text-muted">
        <Help label="Security">How the host connects to your upstream mail server.</Help>
      </label>
      <select id="{id}-tls" class="input" bind:value={draft.tls} onchange={onSecurityChange}>
        {#each TLS_OPTIONS as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
      </select>
    </div>
    <div class="grid gap-1">
      <label for="{id}-user" class="text-muted">Username (optional)</label>
      <input id="{id}-user" class="input" autocomplete="off" bind:value={draft.username} />
    </div>
    <div class="grid gap-1">
      <label for="{id}-pass" class="text-muted">Password (optional)</label>
      <input
        id="{id}-pass"
        class="input"
        type="password"
        autocomplete="new-password"
        placeholder={settings.smtp.password_set ? "Stored; type to replace" : ""}
        bind:value={draft.password}
      />
    </div>
  </div>
</HostCard>
