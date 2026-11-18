/**
 * Stack variables: the values the operator fills in on the host. A manifest
 * declares paths (host directories bound into the stack), addresses (network
 * addresses the stack may reach or be reached from), settings (plain env
 * values) and secrets; the operator may add plain or secret ones too.
 */
import { ProblemError, type Schema, type Stack } from "./api/client";

export type StackVariable = Schema<"StackVariable">;
export type VariableKind = StackVariable["kind"];

/** The daemon's rule for a variable name. */
export const NAME_RE = /^[A-Z][A-Z0-9_]{0,63}$/;

/** Why a name is refused, or "" when it is a valid one. */
export function nameProblem(name: string): string {
  if (!name) return "";
  if (NAME_RE.test(name)) return "";
  if (name.length > 64) return "At most 64 characters.";
  if (!/^[A-Z]/.test(name)) return "Start with a capital letter.";
  return "Use only A-Z, 0-9 and _.";
}

/** A random key: 32 bytes as hex, which every reader takes as is (env-safe). */
export function generateKey(bytes = 32): string {
  const b = crypto.getRandomValues(new Uint8Array(bytes));
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

/** Required variables with no value, stored or a default: what an install or update waits for. */
export function missingVariables(s: Stack): string[] {
  return s.variables.filter((v) => v.required && v.source === "unset").map((v) => v.name);
}

export interface VariableGroups {
  path: StackVariable[];
  address: StackVariable[];
  env: StackVariable[];
  secret: StackVariable[];
  own: StackVariable[];
}

/** Declared variables by kind (in the daemon's manifest order), then the operator's own. */
export function groupVariables(vars: StackVariable[]): VariableGroups {
  const g: VariableGroups = { path: [], address: [], env: [], secret: [], own: [] };
  for (const v of vars) (v.declared ? g[v.kind] : g.own).push(v);
  return g;
}

/** A declared variable is unfilled and the stack cannot be installed or updated without it. */
export const needsValue = (v: StackVariable) => v.required && v.source === "unset";

/** Where a path is mounted in the stack, and how. */
export const mountText = (v: StackVariable) => `${v.mount ?? "?"} · ${v.read_only ? "read-only" : "read-write"}`;

/** Who in the stack takes a path's owner and group, in words; "" when nobody does. */
export function ownerText(v: StackVariable): string {
  if (v.owner && v.group) return `The stack's user ${v.owner} and group ${v.group} take the folder's owner and group.`;
  if (v.owner) return `The stack's user ${v.owner} takes this folder's owner.`;
  if (v.group) return `The stack's group ${v.group} takes this folder's group.`;
  return "";
}

const kindWords: Record<VariableKind, string> = { env: "a setting", secret: "a secret", path: "a path", address: "an address" };

/** A refused save in words, for the row it belongs to. */
export function variableError(e: unknown, v: Pick<StackVariable, "name" | "kind">): string {
  if (!(e instanceof ProblemError)) return e instanceof Error ? e.message : String(e);
  // The daemon prefixes "NAME: "; the row already names it.
  const why = e.detail.startsWith(`${v.name}: `) ? e.detail.slice(v.name.length + 2) : e.detail;
  switch (e.code) {
    case "invalid_path":
      return `${why}. Use an existing directory on the host.`;
    case "invalid_address":
      return `${why}. Use IPv4 addresses or ranges, separated by commas.`;
    case "kind_mismatch":
      return `Declared as ${kindWords[v.kind]}; it cannot be made secret.`;
    case "secret_stays_secret":
      return "A secret stays a secret. Enter a new value instead.";
    default:
      return e.detail || e.message;
  }
}
