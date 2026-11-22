<script lang="ts">
  interface Props {
    /** The icon's address (stackIcon, unitIcon); none draws the letter tile. */
    src: string | undefined;
    /** What the icon stands for: its first letter fills the tile. */
    name: string;
    size: "xs" | "sm" | "md" | "lg";
    /** Greyed: a stopped or disabled service. */
    dim?: boolean;
  }
  let { src, name, size, dim = false }: Props = $props();

  // An address that failed to load falls back to the tile until it changes.
  let failed = $state<string | null>(null);
  const shown = $derived(src && src !== failed ? src : undefined);

  const box = { xs: "size-4", sm: "size-5", md: "size-10", lg: "size-16" };
  const letter = { xs: "text-xs", sm: "text-xs", md: "text-base", lg: "text-xl" };
</script>

<!-- Decorative: the name always sits beside it. -->
{#if shown}
  <img
    src={shown}
    alt=""
    loading="lazy"
    class="{box[size]} shrink-0 object-contain {dim ? 'opacity-45 grayscale' : ''}"
    onerror={() => (failed = shown ?? null)}
  />
{:else}
  <span
    aria-hidden="true"
    class="{box[size]} {letter[size]} inline-flex shrink-0 items-center justify-center rounded-md bg-sunken font-semibold text-muted uppercase {dim ? 'opacity-45' : ''}"
  >
    {name.charAt(0)}
  </span>
{/if}
