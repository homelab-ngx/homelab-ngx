<script lang="ts">
  import { untrack } from "svelte";
  import { api, call } from "#lib/api/client.ts";
  import Help from "#lib/components/Help.svelte";
  import { changed, dashboardHost, DNS_FIELDS, DNS_LABEL, type DNSProvider, type HostSettings } from "#lib/host.ts";
  import HostCard from "./HostCard.svelte";

  interface Props {
    settings: HostSettings;
    readOnly: boolean;
    onSaved: (s: HostSettings) => void;
  }
  let { settings, readOnly, onSaved }: Props = $props();
  const id = $props.id();
  const stored = () => {
    const d = settings.domain;
    return { domain: d.domain, dashboard_label: d.dashboard_label, acme_email: d.acme_email, dns_provider: d.dns_provider, region: d.region ?? "", creds: {} as Record<string, string> };
  };
  let draft = $state(untrack(stored));
  const fields = $derived(DNS_FIELDS[draft.dns_provider ?? ""] ?? []);
  /** New credentials are needed for a provider that has none stored yet. */
  const needCreds = $derived(fields.length > 0 && (draft.dns_provider !== settings.domain.dns_provider || !settings.domain.dns_credentials_set));
  const typed = $derived(fields.some((f) => draft.creds[f.key]));
  const problem = $derived(
    needCreds && fields.some((f) => !draft.creds[f.key]?.trim())
      ? `${DNS_LABEL[draft.dns_provider ?? ""]} needs ${fields.map((f) => f.label.toLowerCase()).join(" and ")}.`
      : "",
  );

  async function apply() {
    const { creds, ...rest } = draft;
    const body = { ...rest, dns_provider: rest.dns_provider as DNSProvider, dns_credentials: typed || needCreds ? creds : undefined };
    onSaved(await call(api.PUT("/host-settings/domain", { body })));
  }
</script>

<HostCard
  anchor="domain"
  title="Domain"
  subtitle="Where the dashboard and stacks are served, with HTTPS."
  help="The private domain the dashboard and every stack route are served under, with one wildcard certificate. Point it at this host in the LAN's DNS."
  {readOnly}
  dirty={changed(draft, stored())}
  {problem}
  {apply}
  reset={() => (draft = stored())}
>
  <div class="grid gap-3 sm:grid-cols-2">
    <div class="grid gap-1">
      <label for="{id}-domain" class="text-muted">Domain</label>
      <input id="{id}-domain" class="input" autocomplete="off" placeholder="home.example.com" bind:value={draft.domain} />
    </div>
    <div class="grid gap-1">
      <label for="{id}-label" class="text-muted">
        <Help label="Dashboard name">The dashboard is served at this name under the domain.</Help>
      </label>
      <input id="{id}-label" class="input" autocomplete="off" placeholder="homelab" bind:value={draft.dashboard_label} />
      {#if dashboardHost(draft)}<span class="text-xs text-muted">{dashboardHost(draft)}</span>{/if}
    </div>
    <div class="grid gap-1">
      <label for="{id}-dns" class="text-muted">
        <Help label="DNS provider">Where the domain's DNS is hosted. Caddy proves the domain there for a wildcard certificate (DNS-01).</Help>
      </label>
      <select id="{id}-dns" class="input" bind:value={draft.dns_provider}>
        {#each Object.entries(DNS_LABEL) as [v, l] (v)}<option value={v}>{l}</option>{/each}
      </select>
    </div>
    <div class="grid gap-1">
      <label for="{id}-email" class="text-muted">Contact email (optional)</label>
      <input id="{id}-email" class="input" type="email" autocomplete="off" bind:value={draft.acme_email} />
    </div>
    {#each fields as f (f.key)}
      <div class="grid gap-1">
        <label for="{id}-{f.key}" class="text-muted">{f.label}</label>
        <input
          id="{id}-{f.key}"
          class="input"
          type="password"
          autocomplete="new-password"
          placeholder={needCreds ? "" : "Stored; type to replace"}
          bind:value={draft.creds[f.key]}
        />
      </div>
    {/each}
    {#if draft.dns_provider === "route53"}
      <div class="grid gap-1">
        <label for="{id}-region" class="text-muted">Region</label>
        <input id="{id}-region" class="input" autocomplete="off" placeholder="eu-north-1" bind:value={draft.region} />
      </div>
    {/if}
  </div>
</HostCard>
