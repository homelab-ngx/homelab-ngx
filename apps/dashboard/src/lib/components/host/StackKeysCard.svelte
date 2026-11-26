<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { describe } from "#lib/api/client.ts";
  import { fetchStackSSHKeys, keys } from "#lib/api/queries.ts";
  import Help from "#lib/components/Help.svelte";

  const sshKeys = createQuery(() => ({ queryKey: keys.sshKeys, queryFn: fetchStackSSHKeys }));
  let copied = $state("");

  async function copy(id: string, key: string) {
    try {
      if (navigator.clipboard) await navigator.clipboard.writeText(key);
      copied = id;
    } catch {
      copied = "";
    }
  }
</script>

<!-- Host SSH deploy key and per-stack keys used for private stack repositories. -->
<section id="stack-keys" class="card scroll-mt-16" aria-labelledby="stack-keys-h">
  <h2 id="stack-keys-h" class="font-semibold">
    <Help label="Private stack repositories">
      This host fetches private stack repositories over SSH with its own read-only deploy key. Adding this key to a user or
      machine account covers every repository that account reaches, with that account's permissions.
    </Help>
  </h2>
  <p class="mt-1 text-xs text-muted">Add per repository as a deploy key, or to a user or machine account.</p>
  <p class="mt-1 text-xs text-muted">An account key grants read access to every repository that account reaches.</p>

  {#if sshKeys.error}
    <p role="alert" class="mt-3 rounded-md bg-bad-soft px-3 py-2 text-bad">{describe(sshKeys.error)}</p>
  {:else if sshKeys.data}
    {@const data = sshKeys.data}
    <div class="mt-3">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div class="min-w-0">
          <span class="label">Host key</span>
          <span class="ml-2 text-xs text-muted">{data.host.fingerprint}</span>
        </div>
        <button type="button" class="btn btn-sm" onclick={() => copy("host", data.host.public_key)}>
          {copied === "host" ? "Copied" : "Copy"}
        </button>
      </div>
      <pre class="mt-2 overflow-x-auto rounded-md bg-sunken p-3 text-xs">{data.host.public_key}</pre>
    </div>

    {#if data.stacks.length > 0}
      <div class="mt-4">
        <h3 class="label">Per-stack keys</h3>
        <ul class="mt-2 divide-y divide-line">
          {#each data.stacks as k (k.stack ?? k.fingerprint)}
            <li class="flex flex-wrap items-center justify-between gap-2 py-2">
              <div class="min-w-0">
                {#if k.stack}
                  <a class="link font-medium" href="/stacks/{encodeURIComponent(k.stack)}">{k.stack}</a>
                {/if}
                {#if k.repo}<span class="ml-2 text-xs text-muted">{k.repo}</span>{/if}
                <p class="text-xs text-muted">{k.fingerprint}</p>
              </div>
              <button type="button" class="btn btn-sm" onclick={() => copy(k.stack ?? k.fingerprint, k.public_key)}>
                {copied === (k.stack ?? k.fingerprint) ? "Copied" : "Copy"}
              </button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  {:else}
    <p class="mt-3 text-muted">Loading…</p>
  {/if}
</section>
