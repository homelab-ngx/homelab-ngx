/** The stack editor's logic, apart from its pages. */
import type { Draft, DraftFiles } from "./api/client.ts";
import { FILES, type FileKey } from "./editor.ts";

/** The files whose text in the buffer differs from the saved one. */
export const changedFiles = (saved: DraftFiles, buffer: DraftFiles): FileKey[] =>
  FILES.filter((f) => saved[f.key] !== buffer[f.key]).map((f) => f.key);

/** A build that has not ended. */
export const building = (d: Draft | undefined) => d?.build?.result === "running";

/** The path a check problem names, as the file key. */
export const fileOf = (path: string): FileKey => (path === "flake.nix" ? "flake" : "manifest");

/** Files an answer proposes, by key; absent is unchanged. */
export type Proposal = Partial<Record<FileKey, string>>;

/** The proposed files that differ from what the editor holds now. */
export const proposedChanges = (buffer: DraftFiles, proposal: Proposal): FileKey[] =>
  FILES.filter((f) => proposal[f.key] !== undefined && proposal[f.key] !== buffer[f.key]).map((f) => f.key);

/** The editor's files with the proposal for key (or every file) put in. */
export function applyProposal(buffer: DraftFiles, proposal: Proposal, key?: FileKey): DraftFiles {
  const next = { ...buffer };
  for (const k of key ? [key] : proposedChanges(buffer, proposal)) {
    const v = proposal[k];
    if (v !== undefined) next[k] = v;
  }
  return next;
}
