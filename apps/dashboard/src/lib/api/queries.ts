/**
 * Query keys and fetchers shared by the pages. A mutation invalidates
 * `keys.all`, so every view of stacks and services reads again.
 */
import { api, call, type AssistantLimits, type AssistantSettings, type Draft, type DraftSummary, type Schema, type ServiceInfo, type Stack, type StackUpdate } from "./client";

export const keys = {
  all: ["daemon"] as const,
  stacks: ["daemon", "stacks"] as const,
  services: ["daemon", "services"] as const,
  updates: (stack: string) => ["daemon", "stack-updates", stack] as const,
  update: (id: number) => ["daemon", "stack-update", id] as const,
  channels: ["daemon", "nixpkgs-channels"] as const,
  nixStore: ["daemon", "nix-store"] as const,
  drafts: ["daemon", "drafts"] as const,
  assistant: ["daemon", "settings", "assistant"] as const,
  manifestSchema: ["daemon", "stacks", "manifest-schema"] as const,
  assistantLimits: ["daemon", "settings", "assistant", "limits"] as const,
  draft: (name: string) => ["daemon", "drafts", name] as const,
};

export const fetchStacks = () => call<Stack[]>(api.GET("/stacks"));
export const fetchStack = (name: string) => call<Stack>(api.GET("/stacks/{name}", { params: { path: { name } } }));
export const fetchServices = () => call<ServiceInfo[]>(api.GET("/services"));
export const fetchUpdates = (stack = "", limit = 50) =>
  call<StackUpdate[]>(api.GET("/stack-updates", { params: { query: { stack: stack || undefined, limit } } }));
export const fetchChannels = () => call<{ channels: string[] }>(api.GET("/stacks-nixpkgs")).then((r) => r.channels);
export const fetchNixStore = () => call<Schema<"NixStore">>(api.GET("/stacks-store"));
export const fetchAssistant = () => call<AssistantSettings>(api.GET("/settings/assistant"));
export const fetchAssistantLimits = () => call<AssistantLimits>(api.GET("/settings/assistant/limits"));
export const fetchDrafts = () => call<DraftSummary[]>(api.GET("/drafts"));
export const fetchDraft = (name: string) => call<Draft>(api.GET("/drafts/{name}", { params: { path: { name } } }));
export const fetchUpdate = (id: number) =>
  call<StackUpdate>(api.GET("/stack-updates/{id}", { params: { path: { id } } }));

/** Polling: fast while something runs, slow otherwise. */
export const busyPoll = 2_000;
export const idlePoll = 15_000;

/** A stack has an operation that has not finished. */
export const busy = (s: Stack) => !!s.operation && !s.operation.finished_at;
