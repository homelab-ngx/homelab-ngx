<script lang="ts">
  import { createInfiniteQuery } from "@tanstack/svelte-query";
  import { untrack } from "svelte";
  import { segments } from "#lib/ansi.ts";
  import { api, call, describe, type Schema } from "#lib/api/client.ts";
  import { stamp } from "#lib/format.ts";
  import AskButton from "./ask/AskButton.svelte";

  type LogPage = Schema<"LogPage">;

  export interface LogUnit {
    id: string;
    label: string;
    /** A heading the picker groups units under. */
    group?: string;
  }

  interface Props {
    /** The units the picker offers; one unit hides the picker. */
    units: LogUnit[];
    /** The unit shown first. */
    initial: string;
    /** Inside a dialog: the lines scroll in a box of their own. */
    boxed?: boolean;
  }
  let { units, initial, boxed = false }: Props = $props();

  const PAGE_SIZE = 100;
  const FOLLOW_MS = 5_000;

  /** Highest syslog priority each level keeps. */
  const LEVELS = [
    { id: "", label: "All levels", priority: undefined },
    { id: "error", label: "Errors", priority: 3 },
    { id: "warn", label: "Warnings and errors", priority: 4 },
    { id: "info", label: "Info and up", priority: 6 },
  ] as const;

  // The picker owns the choice from here; `initial` only seeds it.
  let unit = $state(untrack(() => initial));
  let level = $state("");
  let text = $state("");
  let search = $state("");
  let follow = $state(false);

  // Debounce the search box into the query.
  $effect(() => {
    const t = text.trim();
    const timer = setTimeout(() => (search = t), 300);
    return () => clearTimeout(timer);
  });

  const groups = $derived.by(() => {
    const out: { title: string; members: LogUnit[] }[] = [];
    for (const u of units) {
      const title = u.group ?? "";
      const g = out.find((x) => x.title === title) ?? out[out.push({ title, members: [] }) - 1]!;
      g.members.push(u);
    }
    return out;
  });
  const label = $derived(units.find((u) => u.id === unit)?.label ?? unit);

  const priority = $derived(LEVELS.find((l) => l.id === level)?.priority);
  const logs = createInfiniteQuery(() => ({
    queryKey: ["daemon", "logs", unit, search, level],
    queryFn: ({ pageParam }: { pageParam: string }) =>
      call<LogPage>(
        api.GET("/logs", {
          params: { query: { unit, lines: PAGE_SIZE, before: pageParam || undefined, priority, q: search || undefined } },
        }),
      ),
    initialPageParam: "",
    getNextPageParam: (last: LogPage) => (last.more ? last.entries.at(-1)?.cursor : undefined),
    refetchInterval: follow ? FOLLOW_MS : false,
  }));
  const lines = $derived(logs.data?.pages.flatMap((p) => p.entries) ?? []);

  const levelOf = (p: number) =>
    p <= 3
      ? { word: "ERROR", cls: "text-bad" }
      : p === 4
        ? { word: "WARN", cls: "text-warn" }
        : p === 7
          ? { word: "DEBUG", cls: "text-muted" }
          : { word: "INFO", cls: "text-muted" };
</script>

<div class="flex min-h-0 flex-col gap-3">
  <div class="flex flex-wrap items-center gap-2">
    {#if units.length > 1}
      <label class="min-w-40 flex-1 sm:max-w-64">
        <span class="sr-only">Service</span>
        <select class="input" bind:value={unit}>
          {#if !units.some((u) => u.id === unit)}
            <option value={unit}>{unit}</option>
          {/if}
          {#each groups as g (g.title)}
            {#if g.title}
              <optgroup label={g.title}>
                {#each g.members as u (u.id)}<option value={u.id}>{u.label}</option>{/each}
              </optgroup>
            {:else}
              {#each g.members as u (u.id)}<option value={u.id}>{u.label}</option>{/each}
            {/if}
          {/each}
        </select>
      </label>
    {/if}
    <label class="min-w-40 flex-[2]">
      <span class="sr-only">Search</span>
      <input class="input" type="search" placeholder="Search" bind:value={text} />
    </label>
    <label class="w-full sm:w-48">
      <span class="sr-only">Level</span>
      <select class="input" bind:value={level}>
        {#each LEVELS as l (l.id)}<option value={l.id}>{l.label}</option>{/each}
      </select>
    </label>
    <label class="flex items-center gap-1.5">
      <input type="checkbox" class="accent-accent" bind:checked={follow} />
      Follow
    </label>
    <button type="button" class="btn" disabled={logs.isFetching} onclick={() => logs.refetch()}>
      {logs.isFetching ? "Reading…" : "Refresh"}
    </button>
    {#if lines.length}
      <AskButton title="{label} logs" subject={{ kind: "logs", ref: unit, q: search || undefined, priority }} />
    {/if}
  </div>

  <section
    aria-label="Log lines of {label}"
    class="rounded-md border border-line bg-sunken p-3 font-mono text-xs leading-5 {boxed ? 'h-[60vh] overflow-auto' : ''}"
  >
    {#if logs.error}
      <p role="alert" class="text-bad">{describe(logs.error)}</p>
    {:else if logs.isPending}
      <p class="text-muted">Reading lines…</p>
    {:else if lines.length === 0}
      <p class="text-muted">No lines{search ? ` matching "${search}"` : ""} from {label}.</p>
    {:else}
      <ol>
        {#each lines as l (l.cursor)}
          {@const lv = levelOf(l.priority)}
          <li class="flex items-baseline gap-3 py-px">
            <time datetime={l.time} class="shrink-0 whitespace-nowrap text-muted">{stamp(l.time)}</time>
            <span class="w-11 shrink-0 {lv.cls}">{lv.word}</span>
            <span class="min-w-0 flex-1 break-words whitespace-pre-wrap"
              >{#each segments(l.message) as seg, i (i)}{#if seg.cls}<span class={seg.cls}>{seg.text}</span>{:else}{seg.text}{/if}{/each}</span
            >
          </li>
        {/each}
      </ol>
      {#if logs.hasNextPage}
        <button type="button" class="btn btn-sm mt-3" disabled={logs.isFetchingNextPage} onclick={() => logs.fetchNextPage()}>
          {logs.isFetchingNextPage ? "Loading…" : "Load older"}
        </button>
      {/if}
    {/if}
  </section>
</div>
