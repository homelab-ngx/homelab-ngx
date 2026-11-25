/** Overview page logic: surfacing what needs attention across the host and first-run setup. */
import type { Schema, ServiceInfo, Stack } from "./api/client.ts";
import { uncovered, type Backup, type Coverage } from "./backups.ts";
import type { Tone } from "./components/Pill.svelte";
import type { HostSettings } from "./host.ts";
import { failingOf, openOf, type Snapshot } from "./notifications.ts";
import { logId } from "./stacks.ts";
import { diskState, flaggedDisks } from "./storage.ts";

export interface AttentionItem {
  id: string;
  tone: Tone;
  label: string;
  href?: string;
  action?: "notifications";
}

export interface AttentionInput {
  services?: ServiceInfo[];
  coreServices?: ServiceInfo[];
  notifications?: Snapshot;
  stacks?: Stack[];
  settingsStatus?: Schema<"SettingsStatus">;
  storage?: Schema<"StorageStatus">;
  coverage?: Coverage;
}

const plural = (n: number, singular: string, pluralWord = `${singular}s`) => `${n} ${n === 1 ? singular : pluralWord}`;

/** Items shown in Overview's Needs attention strip, ordered worst first. */
export function attentionItems(input: AttentionInput): AttentionItem[] {
  const items: AttentionItem[] = [];

  const failedStacks = (input.services ?? []).filter((u) => !u.masked && u.active_state === "failed");
  if (failedStacks.length > 0) {
    const stackNames = [...new Set(failedStacks.map((u) => u.stack).filter((s): s is string => !!s))];
    const href = stackNames.length === 1 ? `/stacks/${stackNames[0]}` : "/stacks";
    items.push({ id: "failed-services", tone: "bad", label: plural(failedStacks.length, "failed service"), href });
  }

  const failedCore = (input.coreServices ?? []).filter((u) => !u.masked && u.active_state === "failed");
  if (failedCore.length > 0) {
    const href =
      failedCore.length === 1 ? `/settings/logs?unit=${encodeURIComponent(logId(failedCore[0]!.unit_name))}` : "/settings/logs";
    items.push({ id: "failed-host-services", tone: "bad", label: plural(failedCore.length, "failed host service"), href });
  }

  if (input.settingsStatus?.tiles?.updates?.state === "bad") {
    items.push({ id: "core-update-failed", tone: "bad", label: "Update fell back", href: "/settings/updates" });
  }

  const failingChannels = failingOf(input.notifications);
  if (failingChannels.length > 0) {
    items.push({
      id: "failing-channels",
      tone: "bad",
      label: plural(failingChannels.length, "failing channel"),
      href: "/settings/notifications",
    });
  }

  const openProblems = openOf(input.notifications);
  if (openProblems.length > 0) {
    items.push({
      id: "open-problems",
      tone: "bad",
      label: plural(openProblems.length, "open problem"),
      action: "notifications",
    });
  }

  const badPoolDisks = (input.storage?.pools ?? []).flatMap((p) => flaggedDisks(p.disks));
  const badSpareDisks = (input.storage?.unassigned ?? []).filter((d) => {
    const t = diskState(d).tone;
    return t === "bad" || t === "warn";
  });
  const totalDegraded = badPoolDisks.length + badSpareDisks.length;
  if (totalDegraded > 0) {
    const hasBad =
      badPoolDisks.some((d) => d.state === "failed" || diskState(d).tone === "bad") ||
      badSpareDisks.some((d) => diskState(d).tone === "bad");
    items.push({
      id: "degraded-disks",
      tone: hasBad ? "bad" : "warn",
      label: plural(totalDegraded, "degraded disk"),
      href: "/storage",
    });
  }

  const stackUpdates = (input.stacks ?? []).filter((s) => s.generation > 0 && s.update_state !== "up_to_date");
  if (stackUpdates.length > 0) {
    const needsApproval = stackUpdates.some((s) => s.update_state === "needs_approval");
    const href = stackUpdates.length === 1 ? `/stacks/${stackUpdates[0]!.name}` : "/stacks";
    items.push({
      id: "stack-updates",
      tone: needsApproval ? "warn" : "info",
      label: plural(stackUpdates.length, "stack update"),
      href,
    });
  }

  if (input.settingsStatus?.tiles?.updates?.state === "info") {
    items.push({ id: "core-update", tone: "info", label: "Core update ready", href: "/settings/updates" });
  }

  const unbacked = uncovered(input.coverage);
  if (unbacked.length > 0) {
    items.push({
      id: "uncovered-backups",
      tone: "warn",
      label: `${unbacked.length} not backed up`,
      href: "/backups",
    });
  }

  const rank: Record<Tone, number> = { bad: 0, warn: 1, info: 2, ok: 3, idle: 4 };
  return items.sort((a, b) => rank[a.tone] - rank[b.tone]);
}

export interface SetupStep {
  id: string;
  label: string;
  href: string;
  done: boolean;
}

export interface SetupInput {
  storage?: Schema<"StorageStatus">;
  host?: HostSettings;
  stacks?: Stack[];
  backups?: Backup[];
}

/** First-run setup steps for an unconfigured host; empty until all inputs load or once every step is done. */
export function setupChecklist(input: SetupInput): SetupStep[] {
  if (!input.storage || !input.host || !input.stacks || !input.backups) return [];
  const steps: SetupStep[] = [
    { id: "storage", label: "Create a storage pool", href: "/storage", done: input.storage.pools.length > 0 },
    {
      id: "remote",
      label: "Set up a domain or VPN",
      href: "/settings/domain",
      done: Boolean(input.host.domain?.domain || input.host.wireguard?.enabled || input.host.tunnel?.enabled || input.host.tailscale?.enabled),
    },
    { id: "stack", label: "Add a stack", href: "/stacks/add", done: input.stacks.length > 0 },
    { id: "backup", label: "Configure a backup", href: "/backups", done: input.backups.length > 0 },
  ];
  return steps.every((s) => s.done) ? [] : steps;
}
