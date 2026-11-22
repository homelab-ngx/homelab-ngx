/** The stack editor's logic, apart from its pages. */
import type { Draft, DraftChange, DraftFile } from "./api/client.ts";

/** The files every stack has; neither can be renamed or deleted. */
export const MANIFEST = "homelab-stack.json";
export const FLAKE = "flake.nix";
export const REQUIRED = [MANIFEST, FLAKE];

/** The editor's copy of a draft's files, by path: the text, or null for a binary file kept as it is. */
export type Buffer = Record<string, string | null>;

export const bufferOf = (files: DraftFile[]): Buffer => Object.fromEntries(files.map((f) => [f.path, f.binary ? null : f.content]));

/**
 * The changes that turn the saved files into the buffer: deletes for files it
 * no longer has, then a write for each new or edited text file.
 */
export function changesOf(saved: DraftFile[], buffer: Buffer): DraftChange[] {
  const deletes: DraftChange[] = [];
  const writes: DraftChange[] = [];
  const known = new Set<string>();
  for (const f of saved) {
    known.add(f.path);
    const v = buffer[f.path];
    if (v === undefined) deletes.push({ path: f.path, delete: true });
    else if (v !== null && (f.binary || v !== f.content)) writes.push({ path: f.path, content: v });
  }
  for (const [path, v] of Object.entries(buffer)) {
    if (!known.has(path)) writes.push({ path, content: v ?? "" });
  }
  return [...deletes, ...writes];
}

/** A build that has not ended. */
export const building = (d: Draft | undefined) => d?.build?.result === "running";

/** Why a new file cannot be at path in the buffer; "" when it can. The daemon checks the same. */
export function pathProblem(path: string, buffer: Buffer): string {
  if (!path) return "Give the file a name.";
  if (path.length > 256) return "Use at most 256 characters.";
  if (path.startsWith("/")) return "Leave out the leading /.";
  if (path.includes("\\")) return "Use / between folders.";
  // eslint-disable-next-line no-control-regex
  if (/[\x00-\x1f\x7f]/.test(path)) return "No control characters.";
  const parts = path.split("/");
  if (parts.some((p) => p === "" || p === ".")) return "No empty parts, ./ or doubled /.";
  if (parts.includes("..")) return "A file stays inside the repo.";
  if (parts.includes(".git")) return "Git keeps .git.";
  if (path in buffer) return `${path} is already there.`;
  for (let i = 1; i < parts.length; i++) {
    const dir = parts.slice(0, i).join("/");
    if (dir in buffer) return `${dir} is a file.`;
  }
  if (Object.keys(buffer).some((p) => p.startsWith(path + "/"))) return `${path} is a folder.`;
  return "";
}

/** One line of the file list: a folder heading or a file, indented by depth. */
export interface TreeRow {
  kind: "folder" | "file";
  path: string;
  name: string;
  depth: number;
}

/** The paths as a tree, folders before files at each level, each by name. */
export function fileTree(paths: string[]): TreeRow[] {
  interface Node {
    folders: Map<string, Node>;
    files: string[];
  }
  const root: Node = { folders: new Map(), files: [] };
  for (const p of paths) {
    const parts = p.split("/");
    const name = parts.pop() ?? p;
    let n = root;
    for (const part of parts) {
      let next = n.folders.get(part);
      if (!next) n.folders.set(part, (next = { folders: new Map(), files: [] }));
      n = next;
    }
    n.files.push(name);
  }
  const rows: TreeRow[] = [];
  const walk = (n: Node, prefix: string, depth: number) => {
    for (const name of [...n.folders.keys()].sort()) {
      const path = prefix + name;
      rows.push({ kind: "folder", path, name, depth });
      walk(n.folders.get(name)!, path + "/", depth + 1);
    }
    for (const name of n.files.sort()) rows.push({ kind: "file", path: prefix + name, name, depth });
  };
  walk(root, "", 0);
  return rows;
}

/** Whole files an answer proposes, by path. */
export type Proposal = Record<string, string>;

/** The proposed files that differ from what the editor holds now. */
export const proposedChanges = (buffer: Buffer, proposal: Proposal): string[] =>
  Object.keys(proposal).filter((p) => proposal[p] !== buffer[p]);

/** The buffer with the proposal for path (or every proposed file) put in. */
export function applyProposal(buffer: Buffer, proposal: Proposal, path?: string): Buffer {
  const next = { ...buffer };
  for (const p of path ? [path] : proposedChanges(buffer, proposal)) {
    if (proposal[p] !== undefined) next[p] = proposal[p];
  }
  return next;
}
