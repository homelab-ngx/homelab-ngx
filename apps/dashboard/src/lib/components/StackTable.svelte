<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { ChevronRight } from "@lucide/svelte";
  import { Popover } from "bits-ui";
  import { SvelteSet } from "svelte/reactivity";
  import { api, call, describe, type ServiceInfo, type Stack } from "#lib/api/client.ts";
  import { busy, fetchAssistant, keys } from "#lib/api/queries.ts";
  import { bytes, cpu, shortRev } from "#lib/format.ts";
  import { health, logId, nixpkgsOnly, routeLinks, servicesOf, shortName, stackTitle, type StackState } from "#lib/stacks.ts";
  import AskDialog from "./ask/AskDialog.svelte";
  import LogsDialog from "./LogsDialog.svelte";
  import Pill, { type Tone } from "./Pill.svelte";
  import ReviewDialog from "./ReviewDialog.svelte";
  import RowMenu from "./RowMenu.svelte";

  interface Props {
    stacks: Stack[];
    services: ServiceInfo[];
    /** Show every stack's services from the start (the detail page). */
    expandAll?: boolean;
  }
  let { stacks, services, expandAll = false }: Props = $props();

  const client = useQueryClient();
  // Stacks the operator opened. A stack with a failed service is always
  // open: the failure must be visible without a click.
  const opened = new SvelteSet<string>();
  let actionError = $state("");
  /** The stack and service whose logs are open. */
  let logsOf = $state<{ stack: string; title: string; unit: string } | null>(null);
  let reviewing = $state<Stack | null>(null);
  /** The failed service the assistant is asked about. */
  let askOf = $state<{ title: string; unit: string } | null>(null);
  const assistant = createQuery(() => ({ queryKey: keys.assistant, queryFn: fetchAssistant }));
  const assistantOn = $derived(!!assistant.data && assistant.data.provider !== "none");

  const rows = $derived(
    stacks.map((s) => {
      const svcs = servicesOf(services, s.name);
      const h = health(s, svcs);
      return { stack: s, services: svcs, health: h, open: expandAll || opened.has(s.name) || h.failed.length > 0 };
    }),
  );

  const stateTone: Record<StackState, Tone> = {
    running: "ok",
    frozen: "info",
    degraded: "warn",
    stopped: "idle",
    starting: "info",
    "not-installed": "idle",
  };
  const stateLabel: Record<StackState, string> = {
    running: "Running",
    frozen: "Frozen",
    degraded: "Degraded",
    stopped: "Stopped",
    starting: "Starting",
    "not-installed": "Not installed",
  };

  function unitTone(u: ServiceInfo): Tone {
    if (u.active_state === "active") return "ok";
    if (u.active_state === "failed") return "bad";
    if (u.active_state === "activating" || u.active_state === "reloading") return "info";
    return "idle";
  }

  const action = createMutation(() => ({
    mutationFn: ({ unit, verb }: { unit: string; verb: "start" | "stop" | "restart" }) => {
      const params = { params: { path: { unit } } };
      if (verb === "start") return call(api.POST("/services/{unit}/start", params));
      if (verb === "stop") return call(api.POST("/services/{unit}/stop", params));
      return call(api.POST("/services/{unit}/restart", params));
    },
    onMutate: () => (actionError = ""),
    onError: (e) => (actionError = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: keys.all }),
  }));

  const update = createMutation(() => ({
    mutationFn: (name: string) => call(api.POST("/stacks/{name}/update", { params: { path: { name } } })),
    onMutate: () => (actionError = ""),
    onError: (e) => (actionError = describe(e)),
    onSettled: () => client.invalidateQueries({ queryKey: keys.all }),
  }));

  function toggle(name: string) {
    if (opened.has(name)) opened.delete(name);
    else opened.add(name);
  }
</script>

{#if actionError}
  <p role="alert" class="mb-3 rounded-md bg-bad-soft px-3 py-2 text-bad">{actionError}</p>
{/if}

<!-- relative: the sr-only header text stays inside the scroll box, so a
     phone never scrolls the whole page sideways. Columns that are detail
     (version, usage, links) drop out below md. -->
<div class="relative overflow-x-auto rounded-lg border border-line bg-surface">
  <table class="w-full border-collapse text-left tabular-nums md:min-w-[760px]">
    <thead>
      <tr class="border-b border-line bg-sunken">
        <th scope="col" class="label px-3 py-2">{expandAll ? "Service" : "Stack"}</th>
        <th scope="col" class="label px-3 py-2">State</th>
        {#if !expandAll}
          <th scope="col" class="label px-3 py-2 max-md:hidden">Version</th>
          <th scope="col" class="label px-3 py-2">Update</th>
        {/if}
        <th scope="col" class="label px-3 py-2 text-right max-md:hidden">CPU</th>
        <th scope="col" class="label px-3 py-2 text-right max-md:hidden">Memory</th>
        {#if !expandAll}
          <th scope="col" class="label px-3 py-2 max-md:hidden">Open</th>
        {/if}
        <th scope="col" class="px-3 py-2"><span class="sr-only">Actions</span></th>
      </tr>
    </thead>
    <tbody>
      {#each rows as row (row.stack.name)}
        {@const s = row.stack}
        {@const links = routeLinks(s.approved?.manifest)}
        {#if !expandAll}
          <tr class="border-b border-line font-medium last:border-0">
            <td class="px-3 py-2.5">
              <button
                type="button"
                class="inline-flex items-center gap-1.5 hover:text-accent disabled:cursor-default disabled:hover:text-fg"
                aria-expanded={row.open}
                aria-controls={row.services.map((u) => `svc-${u.name}`).join(" ") || undefined}
                onclick={() => toggle(s.name)}
                disabled={row.health.failed.length > 0}
                title={row.health.failed.length > 0 ? "Stays open while a service has failed" : undefined}
              >
                <ChevronRight class="size-3.5 text-muted transition-transform {row.open ? 'rotate-90' : ''}" aria-hidden="true" />
                {stackTitle(s)}
              </button>
            </td>
            <td class="px-3 py-2.5">
              <Pill tone={stateTone[row.health.state]}>
                {stateLabel[row.health.state]}{#if row.health.total > 0 && s.running}&nbsp;{row.health.active}/{row.health.total}{/if}
              </Pill>
            </td>
            <td class="px-3 py-2.5 font-normal whitespace-nowrap max-md:hidden">
              {shortRev(s.generations.find((g) => g.id === s.generation)?.rev)} · gen {s.generation}
            </td>
            <td class="px-3 py-2.5 font-normal">
              {#if s.operation && !s.operation.finished_at}
                <Pill tone="info">{s.operation.kind}…</Pill>
              {:else if s.update_state === "needs_approval"}
                <span class="inline-flex flex-wrap items-center gap-2">
                  <Pill tone="warn">Needs approval</Pill>
                  <button type="button" class="btn btn-sm" onclick={() => (reviewing = s)}>Review…</button>
                </span>
              {:else if s.update_state === "commits_available"}
                <span class="inline-flex flex-wrap items-center gap-2">
                  {#if nixpkgsOnly(s)}
                    <Pill tone="info">New NixOS packages</Pill>
                  {:else}
                    <Pill tone="info">{s.commits.length || "New"} commit{s.commits.length === 1 ? "" : "s"}</Pill>
                  {/if}
                  <button
                    type="button"
                    class="btn btn-sm"
                    disabled={update.isPending || busy(s) || !s.variables_ready}
                    title={s.variables_ready ? undefined : "Variables need values first"}
                    onclick={() => update.mutate(s.name)}>Update</button
                  >
                </span>
              {:else}
                <span class="text-muted">Up to date</span>
              {/if}
            </td>
            <td class="px-3 py-2.5 text-right font-normal max-md:hidden">{cpu(s.usage?.cpu_percent)}</td>
            <td class="px-3 py-2.5 text-right font-normal max-md:hidden">{bytes(s.usage?.memory_bytes)}</td>
            <td class="px-3 py-2.5 font-normal max-md:hidden">
              {#if links.length === 1}
                <a class="link" href={links[0]} target="_blank" rel="noreferrer">{links[0]!.slice(8)}</a>
              {:else if links.length > 1}
                <Popover.Root>
                  <Popover.Trigger class="link">{links.length} links</Popover.Trigger>
                  <Popover.Portal>
                    <Popover.Content sideOffset={6} align="start" class="z-50 min-w-48 rounded-md border border-line bg-surface p-2 shadow-lg">
                      <ul>
                        {#each links as l (l)}
                          <li><a class="link block py-0.5" href={l} target="_blank" rel="noreferrer">{l.slice(8)}</a></li>
                        {/each}
                      </ul>
                    </Popover.Content>
                  </Popover.Portal>
                </Popover.Root>
              {/if}
            </td>
            <td class="px-3 py-2.5 text-right">
              <a class="btn btn-sm" href="/stacks/{s.name}" aria-label="Open {stackTitle(s)}">Open</a>
            </td>
          </tr>
        {/if}
        {#if row.open}
          {#each row.services as u (u.name)}
            <tr id="svc-{u.name}" class="border-b border-line last:border-0 {expandAll ? '' : 'bg-sunken'}">
              <td class="py-1.5 pr-3 {expandAll ? 'pl-3' : 'pl-8 md:pl-10'}">{shortName(s.name, u.name)}</td>
              <td class="px-3 py-1.5"><Pill tone={unitTone(u)}>{u.active_state}</Pill></td>
              {#if !expandAll}
                <td class="px-3 py-1.5 max-md:hidden"></td>
                <td class="px-3 py-1.5"></td>
              {/if}
              <td class="px-3 py-1.5 text-right max-md:hidden">{cpu(u.usage?.cpu_percent)}</td>
              <td class="px-3 py-1.5 text-right max-md:hidden">{bytes(u.usage?.memory_bytes)}</td>
              {#if !expandAll}<td class="px-3 py-1.5 max-md:hidden"></td>{/if}
              <td class="px-3 py-1.5 whitespace-nowrap"><span class="flex justify-end gap-1">
                <button type="button" class="btn btn-sm" onclick={() => (logsOf = { stack: s.name, title: stackTitle(s), unit: logId(u.name) })}>Logs</button>
                <RowMenu
                  name={shortName(s.name, u.name)}
                  items={u.active_state === "active"
                    ? [
                        { label: "Restart", disabled: action.isPending, onSelect: () => action.mutate({ unit: u.name, verb: "restart" }) },
                        { label: "Stop", disabled: action.isPending, onSelect: () => action.mutate({ unit: u.name, verb: "stop" }) },
                      ]
                    : [
                        { label: "Start", disabled: action.isPending || !s.running, onSelect: () => action.mutate({ unit: u.name, verb: "start" }) },
                        ...(assistantOn && u.active_state === "failed"
                          ? [{ label: "Ask…", onSelect: () => (askOf = { title: shortName(s.name, u.name), unit: logId(u.name) }) }]
                          : []),
                      ]}
                />
              </span></td>
            </tr>
          {:else}
            <tr class="border-b border-line last:border-0 {expandAll ? '' : 'bg-sunken'}">
              <td colspan="8" class="py-1.5 pl-10 text-muted">No services listed</td>
            </tr>
          {/each}
        {/if}
      {/each}
    </tbody>
  </table>
</div>

{#if askOf}
  {@const a = askOf}
  <AskDialog title={a.title} subject={{ kind: "service", ref: a.unit }} onClose={() => (askOf = null)} />
{/if}

{#if logsOf}
  {@const open = logsOf}
  <LogsDialog stack={open.stack} title={open.title} services={servicesOf(services, open.stack)} unit={open.unit} onClose={() => (logsOf = null)} />
{/if}
<ReviewDialog stack={reviewing} onClose={() => (reviewing = null)} />
