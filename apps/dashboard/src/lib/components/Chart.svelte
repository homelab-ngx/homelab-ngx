<script lang="ts">
  /**
   * A time-series chart over uPlot (canvas): one line per series, or a
   * stacked area when the series add up to a whole. The legend under the
   * plot doubles as the hover readout: it lists every series' value at the
   * crosshair. The plot is rebuilt when its series or the theme change and
   * only fed new data otherwise.
   */
  import uPlot from "uplot";
  import "uplot/dist/uPlot.min.css";
  import { untrack } from "svelte";
  import type { Attachment } from "svelte/attachments";
  import type { Line } from "#lib/metrics.ts";

  type Props = {
    /** Unix seconds. */
    at: number[];
    lines: Line[];
    /** Draw running sums with the band under each line filled. */
    stacked?: boolean;
    /** Axis ticks and legend values. */
    format: (v: number) => string;
    /** The y axis reaches at least this high, so a quiet line stays low. */
    floor?: number;
    /** The y axis never goes above this (100 for a share). */
    ceiling?: number;
    height?: number;
    /** What the chart shows, for screen readers. */
    label: string;
  };
  let { at, lines, stacked = false, format, floor = 1, ceiling, height = 180, label }: Props = $props();

  const isDark = () => document.documentElement.classList.contains("dark");
  let dark = $state(isDark());
  $effect(() => {
    // The theme is a class on <html>, set by Settings.
    const mo = new MutationObserver(() => (dark = isDark()));
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
    return () => mo.disconnect();
  });

  const token = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  const color = (l: Line) => (dark ? l.hue.dark : l.hue.light);

  /** The plot is rebuilt only when this changes. */
  const shape = $derived(`${dark}|${stacked}|${height}|${lines.map((l) => `${l.label}:${l.hue.light}`).join(",")}`);

  // The raw (not summed) values the legend reads, kept current with the data.
  let raw: Line[] = [];

  function data(): uPlot.AlignedData {
    raw = lines;
    if (!stacked) return [at, ...lines.map((l) => l.values)];
    let sum: number[] = at.map(() => 0);
    const gap = at.map((_, i) => lines.every((l) => l.values[i] === null || l.values[i] === undefined));
    const cols = lines.map((l) => {
      sum = sum.map((s, i) => s + (l.values[i] ?? 0));
      return sum.map((v, i) => (gap[i] ? null : v));
    });
    return [at, ...cols];
  }

  function options(width: number): uPlot.Options {
    const muted = token("--muted");
    const grid = token("--line");
    // The body face (DESIGN.md, The One Family Rule); figures are tabular in the legend.
    const font = `11px ${token("--font-sans") || "system-ui, sans-serif"}`;
    const axis = { stroke: muted, font, grid: { stroke: grid, width: 1 }, ticks: { stroke: grid, width: 1 } };
    return {
      width,
      height,
      cursor: { y: false, points: { size: 8 } },
      legend: { live: true },
      scales: {
        y: {
          range: (_u, _min, max) => {
            const top = Math.max((max ?? 0) * 1.1, floor);
            return [0, ceiling === undefined ? top : Math.min(top, ceiling)];
          },
        },
      },
      axes: [
        axis,
        {
          ...axis,
          values: (_u, splits) => splits.map((v) => format(v)),
          // As wide as the longest tick label, so "10.0 MB/s" is never cut.
          size: (u, values, i) => {
            if (!values?.length) return 40;
            // uPlot keeps the font scaled to the pixel ratio as font[0].
            u.ctx.font = (u.axes[i]!.font as unknown as string[])[0]!;
            return Math.ceil(Math.max(...values.map((v) => u.ctx.measureText(v).width)) / devicePixelRatio) + 18;
          },
        },
      ],
      series: [
        {
          label: "Time",
          value: (_u: uPlot, v: number | null) => (v === null ? "latest" : new Date(v * 1000).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", second: at.length > 1 && (at[1]! - at[0]!) < 60 ? "2-digit" : undefined })),
        },
        ...lines.map((l, i) => ({
          label: l.label,
          stroke: color(l),
          width: stacked ? 1.5 : 2,
          fill: stacked ? `${color(l)}59` : undefined,
          points: { show: false },
          // Away from the plot the legend shows each line's latest value.
          value: (_u: uPlot, _v: number | null, _si: number, idx: number | null) => {
            const v = idx === null ? last(raw[i]?.values) : raw[i]?.values[idx];
            return v === null || v === undefined ? "–" : format(v);
          },
        })),
      ],
      // Each line's fill reaches down to the line under it, not to zero.
      bands: stacked ? lines.slice(1).map((_, i) => ({ series: [i + 2, i + 1] as [number, number] })) : undefined,
    };
  }

  const last = (vs: (number | null)[] | undefined) => vs?.findLast((v) => v !== null);

  let plot: uPlot | undefined;

  const mount: Attachment<HTMLDivElement> = (node) => {
    void shape;
    const u = untrack(() => new uPlot(options(node.clientWidth), data(), node));
    plot = u;
    const ro = new ResizeObserver(() => u.setSize({ width: node.clientWidth, height }));
    ro.observe(node);
    return () => {
      ro.disconnect();
      u.destroy();
      plot = undefined;
    };
  };

  $effect(() => {
    const d = data();
    plot?.setData(d);
  });
</script>

<div class="chart" role="img" aria-label={label} {@attach mount}></div>

<style>
  .chart {
    width: 100%;
    min-width: 0;
  }
  .chart :global(.u-legend) {
    font-size: 12px;
    color: var(--fg);
    text-align: left;
    margin-top: 4px;
  }
  .chart :global(.u-legend .u-series) {
    padding: 2px 6px;
  }
  /* The legend's entries line up in equal columns, the time on its own line above. */
  .chart :global(.u-legend.u-inline tbody) {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(13rem, 1fr));
  }
  .chart :global(.u-legend.u-inline tr) {
    display: flex;
    align-items: baseline;
    gap: 6px;
    margin-right: 0;
  }
  .chart :global(.u-legend.u-inline :is(th, td)) {
    white-space: nowrap;
  }
  .chart :global(.u-legend.u-inline td) {
    margin-left: auto;
  }
  .chart :global(.u-legend.u-inline tr:first-child) {
    grid-column: 1 / -1;
  }
  .chart :global(.u-legend.u-inline tr:first-child td) {
    margin-left: 0;
  }
  .chart :global(.u-legend th) {
    font-weight: 400;
    color: var(--muted);
  }
  .chart :global(.u-legend td) {
    font-variant-numeric: tabular-nums;
  }
  .chart :global(.u-cursor-x) {
    border-right: 1px solid var(--muted);
  }
</style>
