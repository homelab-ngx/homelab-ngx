<script lang="ts">
  import { renderSVG } from "uqr";

  interface Props {
    /** The device's wg-quick config, private key included. */
    config: string;
    /** The file's name, without .conf. */
    name: string;
  }
  let { config, name }: Props = $props();

  // Dark on white in either theme: a phone's camera reads it that way.
  const qr = $derived(renderSVG(config, { border: 2 }));
  const href = $derived(`data:text/plain;charset=utf-8,${encodeURIComponent(config)}`);
  let copied = $state(false);

  async function copy() {
    await navigator.clipboard.writeText(config);
    copied = true;
  }
</script>

<!-- A new VPN device's config, shown once: scanned by the WireGuard app, or
     saved as a file. The SVG is made here from the config, not from input. -->
<div class="grid gap-3">
  <div class="mx-auto w-56 max-w-full rounded-md bg-white p-1" role="img" aria-label="QR code of the config">
    {@html qr}
  </div>
  <pre class="overflow-x-auto rounded-md bg-sunken p-3 text-xs">{config}</pre>
  <div class="flex flex-wrap gap-2">
    <a class="btn btn-sm" {href} download="{name}.conf">Download</a>
    <button type="button" class="btn btn-sm" onclick={copy}>{copied ? "Copied" : "Copy"}</button>
  </div>
</div>
