<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from "@tanstack/svelte-query";
  import { api, call, describe, type ServiceInfo, type Stack } from "#lib/api/client.ts";
  import { fetchAssistant, keys } from "#lib/api/queries.ts";
  import { bytes, cpu } from "#lib/format.ts";
  import { logId, shortName, unitIcon, unitTone, unitWord } from "#lib/stacks.ts";
  import AskDialog from "./ask/AskDialog.svelte";
  import Modal from "./Modal.svelte";
  import Pill from "./Pill.svelte";
  import RowMenu from "./RowMenu.svelte";
  import StackIcon from "./StackIcon.svelte";

  interface Props {
    stack: Stack;
    /** The stack's own services (servicesOf). */
    services: ServiceInfo[];
  }
  let { stack, services }: Props = $props();

  const client = useQueryClient();
  let actionError = $state("");
  /** The service the operator is asked to confirm disabling. */
  let disabling = $state<{ title: string; unit: string } | null>(null);
  let maskError = $state("");
  /** The failed service the assistant is asked about. */
  let askOf = $state<{ title: string; unit: string } | null>(null);
  const assistant = createQuery(() => ({ queryKey: keys.assistant, queryFn: fetchAssistant }));
  const assistantOn = $derived(!!assistant.data && assistant.data.provider !== "none");

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

  const mask = createMutation(() => ({
    mutationFn: ({ unit, on }: { unit: string; on: boolean }) => {
      const params = { params: { path: { unit } } };
      return on ? call(api.PUT("/services/{unit}/mask", params)) : call(api.DELETE("/services/{unit}/mask", params));
    },
    onMutate: () => ((actionError = ""), (maskError = "")),
    onSuccess: () => (disabling = null),
    onError: (e, v) => (v.on ? (maskError = describe(e)) : (actionError = describe(e))),
    onSettled: () => client.invalidateQueries({ queryKey: keys.all }),
  }));

  /** A service's menu: a disabled one can only be enabled again. */
  function unitItems(u: ServiceInfo) {
    if (u.masked) return [{ label: "Enable", disabled: mask.isPending, onSelect: () => mask.mutate({ unit: u.name, on: false }) }];
    const items =
      u.active_state === "active"
        ? [
            { label: "Restart", disabled: action.isPending, onSelect: () => action.mutate({ unit: u.name, verb: "restart" as const }) },
            { label: "Stop", disabled: action.isPending, onSelect: () => action.mutate({ unit: u.name, verb: "stop" as const }) },
          ]
        : [
            { label: "Start", disabled: action.isPending || !stack.running, onSelect: () => action.mutate({ unit: u.name, verb: "start" as const }) },
            ...(assistantOn && u.active_state === "failed"
              ? [{ label: "Ask…", onSelect: () => (askOf = { title: shortName(stack.name, u.name), unit: logId(u.name) }) }]
              : []),
          ];
    // The last enabled service is not disabled: that is stopping the stack.
    const last = services.filter((x) => !x.masked).length <= 1;
    return [
      ...items,
      { label: "Disable…", danger: true, disabled: last, onSelect: () => ((maskError = ""), (disabling = { title: shortName(stack.name, u.name), unit: u.name })) },
    ];
  }
</script>

{#if actionError}
  <p role="alert" class="mb-3 rounded-md bg-bad-soft px-3 py-2 text-bad">{actionError}</p>
{/if}

<!-- One stack's services (DESIGN.md, Stack Page). A row's actions are in its
     menu, Disable… last. Disk is what the service's user owns in the stack. -->
<div class="relative overflow-x-auto">
  <table class="w-full border-collapse text-left tabular-nums">
    <thead>
      <tr class="border-y border-line bg-sunken">
        <th scope="col" class="label px-4 py-2">Service</th>
        <th scope="col" class="label px-3 py-2">State</th>
        <th scope="col" class="label px-3 py-2 text-right max-sm:hidden">CPU</th>
        <th scope="col" class="label px-3 py-2 text-right max-sm:hidden">Memory</th>
        <th scope="col" class="label px-3 py-2 text-right max-sm:hidden">Disk</th>
        <th scope="col" class="px-3 py-2"><span class="sr-only">Actions</span></th>
      </tr>
    </thead>
    <tbody>
      {#each services as u (u.name)}
        {@const tone = unitTone(u)}
        <tr class="border-b border-line last:border-0 {tone === 'bad' ? 'bg-bad-soft' : ''}">
          <td class="py-1.5 pr-3 pl-4">
            <span class="flex min-w-0 items-center gap-2.5">
              <StackIcon src={unitIcon(stack, u)} name={shortName(stack.name, u.name)} size="sm" dim={tone === "idle"} />
              <span class="truncate">{shortName(stack.name, u.name)}</span>
            </span>
          </td>
          <td class="px-3 py-1.5"><Pill {tone}>{unitWord(u)}</Pill></td>
          <td class="px-3 py-1.5 text-right max-sm:hidden">{cpu(u.usage?.cpu_percent)}</td>
          <td class="px-3 py-1.5 text-right max-sm:hidden">{bytes(u.usage?.memory_bytes)}</td>
          <td class="px-3 py-1.5 text-right max-sm:hidden">{bytes(u.disk_bytes)}</td>
          <td class="px-3 py-1.5 text-right">
            <RowMenu name={shortName(stack.name, u.name)} items={unitItems(u)} />
          </td>
        </tr>
      {:else}
        <tr><td colspan="6" class="px-4 py-2 text-muted">No services listed</td></tr>
      {/each}
    </tbody>
  </table>
</div>

<Modal
  open={disabling !== null}
  onOpenChange={(o) => {
    if (!o) disabling = null;
  }}
  title="Disable {disabling?.title}?"
  description="Stops now and stays stopped after restarts and updates."
>
  <p class="text-muted">A route or health check it serves stops answering. Enable brings it back.</p>
  {#if maskError}<p role="alert" class="mt-2 text-bad">{maskError}</p>{/if}
  {#snippet actions()}
    <button type="button" class="btn btn-danger" disabled={mask.isPending} onclick={() => disabling && mask.mutate({ unit: disabling.unit, on: true })}>Disable</button>
  {/snippet}
</Modal>

{#if askOf}
  {@const a = askOf}
  <AskDialog title={a.title} subject={{ kind: "service", ref: a.unit }} onClose={() => (askOf = null)} />
{/if}
