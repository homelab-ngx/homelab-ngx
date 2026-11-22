<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { ArrowLeft, Code, History, Info, ScrollText, Shield, SlidersHorizontal } from "@lucide/svelte";
  import type { Snippet } from "svelte";
  import { page } from "$app/state";
  import { busy, busyPoll, fetchStack, idlePoll, keys } from "#lib/api/queries.ts";
  import StackIcon from "#lib/components/StackIcon.svelte";
  import StatusMark from "#lib/components/StatusMark.svelte";
  import { stackIcon, stackTitle } from "#lib/stacks.ts";

  let { children }: { children: Snippet } = $props();

  const name = $derived(page.params.name ?? "");
  const stack = createQuery(() => ({
    queryKey: [...keys.stacks, name],
    queryFn: () => fetchStack(name),
    refetchInterval: (q) => (q.state.data && busy(q.state.data) ? busyPoll : idlePoll),
  }));

  const base = $derived(`/stacks/${name}`);
  const tabs = $derived([
    { href: base, label: "Info", Icon: Info },
    { href: `${base}/configuration`, label: "Configuration", Icon: SlidersHorizontal },
    { href: `${base}/source`, label: "Source code", Icon: Code },
    { href: `${base}/logs`, label: "Logs", Icon: ScrollText },
    { href: `${base}/history`, label: "History", Icon: History },
    { href: `${base}/access`, label: "Access", Icon: Shield },
  ]);
  /** Configuration carries a warn mark while a value is missing. */
  const needsValues = $derived(stack.data ? !stack.data.variables_ready : false);
</script>

<!-- A stack's page is tabs, each its own address (DESIGN.md, Stack Page). -->
<nav aria-label="Stack" class="flex items-stretch overflow-x-auto border-b border-line">
  <a href="/stacks" class="flex shrink-0 items-center pr-3 text-muted hover:text-fg" aria-label="Back to Stacks">
    <ArrowLeft class="size-4" aria-hidden="true" />
  </a>
  <span class="mr-2 flex min-w-0 shrink-0 items-center gap-2 border-l border-line pl-3 pr-2 font-semibold">
    {#if stack.data}<StackIcon src={stackIcon(stack.data)} name={stackTitle(stack.data)} size="xs" />{/if}
    <span class="max-w-40 truncate">{stack.data ? stackTitle(stack.data) : name}</span>
  </span>
  {#each tabs as t (t.href)}
    {@const on = page.url.pathname === t.href}
    <a
      href={t.href}
      aria-current={on ? "page" : undefined}
      class="-mb-px flex shrink-0 items-center gap-1.5 border-b-2 px-3 py-2.5 font-medium whitespace-nowrap focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent {on
        ? 'border-accent text-accent'
        : 'border-transparent text-muted hover:text-fg'}"
    >
      <t.Icon class="size-4" aria-hidden="true" />{t.label}
      {#if t.label === "Configuration" && needsValues}<StatusMark tone="warn" /><span class="sr-only">(needs values)</span>{/if}
    </a>
  {/each}
</nav>

<div class="mt-5">{@render children()}</div>
