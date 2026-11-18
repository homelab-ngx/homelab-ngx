<script lang="ts">
  import type { DayMark, DayTone } from "#lib/backups.ts";

  interface Props {
    /** One mark per day, oldest first (dayMarks). */
    days: DayMark[];
    /** Where a run opens. */
    href: (runId: number) => string;
    /** The run that is open, to mark its day. */
    selected?: number | null;
    /** Taller, wider marks for a backup's own page, where a mark changes the run shown without moving the page. */
    large?: boolean;
    /** What the strip belongs to, for its accessible name. */
    name: string;
  }
  let { days, href, selected = null, large = false, name }: Props = $props();

  // A day reads without colour: how much of the column is filled says how it
  // went. Full is a good day, two thirds a partly failed one, a short stub a
  // failed one, and an empty column a day without a run.
  const track: Record<DayTone, string> = {
    ok: "bg-ok-soft",
    warn: "bg-warn-soft",
    bad: "bg-bad-soft",
    run: "bg-accent-soft",
    // An empty day keeps an outline: sunken on surface is too faint in the dark theme.
    none: "bg-sunken inset-ring inset-ring-line",
  };
  const fill: Record<DayTone, string> = {
    ok: "h-full bg-ok",
    warn: "h-2/3 bg-warn",
    bad: "h-1/4 bg-bad",
    run: "h-full bg-accent motion-safe:animate-pulse",
    none: "h-px bg-line",
  };
  const size = $derived(large ? "h-9 w-4 max-sm:w-3" : "h-5 w-2.5 max-sm:w-2");
</script>

<ol class="flex items-end {large ? 'gap-1' : 'gap-[3px]'}" aria-label="{name}: the last {days.length} days">
  {#each days as d (d.key)}
    <li class="flex shrink">
      {#if d.run}
        <a
          href={href(d.run.id)}
          title={d.label}
          aria-label={d.label}
          aria-current={d.run.id === selected ? "true" : undefined}
          data-sveltekit-reset={large ? "false" : undefined}
          data-sveltekit-replacestate={large ? "" : undefined}
          class="flex items-end overflow-hidden rounded-sm outline-offset-2 hover:outline-2 hover:outline-line focus-visible:outline-2 focus-visible:outline-accent aria-[current=true]:outline-2 aria-[current=true]:outline-accent {size} {track[d.tone]}"
        >
          <span aria-hidden="true" class="w-full {fill[d.tone]}"></span>
        </a>
      {:else}
        <span title={d.label} class="flex items-end overflow-hidden rounded-sm {size} {track[d.tone]}">
          <span class="sr-only">{d.label}</span>
          <span aria-hidden="true" class="w-full {fill[d.tone]}"></span>
        </span>
      {/if}
    </li>
  {/each}
</ol>
