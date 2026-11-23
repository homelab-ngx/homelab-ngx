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
export const mountText = (v: StackVariable) =>
  `${v.mount ?? "?"} · ${v.read_only_by_operator ? "read-only (set on this host)" : v.read_only ? "read-only" : "read-write"}`;

/** The manifest itself binds the path read-only, so it cannot be made writable here. */
export const declaredReadOnly = (v: StackVariable) => !!v.read_only && !v.read_only_by_operator;

/** Who in the stack takes a path's owner and group, in words; "" when nobody does. */
export function ownerText(v: StackVariable): string {
  if (v.owner && v.group) return `The stack's user ${v.owner} and group ${v.group} take the folder's owner and group.`;
  if (v.owner) return `The stack's user ${v.owner} takes this folder's owner.`;
  if (v.group) return `The stack's group ${v.group} takes this folder's group.`;
  return "";
}

/** Who owns a path's folder now, in words; "" when the stack takes no owner from it or it has none yet. */
export function ownedText(v: StackVariable): string {
  const who = `${v.folder_uid ?? "?"}:${v.folder_gid ?? "?"}`;
  switch (v.owner_state) {
    case "ok":
      return `The folder has this host's ids for them (${who}).`;
    case "own":
      return `The folder came with ids of its own (${who}), and the stack uses those.`;
    case "root":
      return `The folder is owned by root (${who}), which the stack cannot use. Change owner, in the row's menu, gives it this host's ids.`;
    case "group_differs":
      return `Another stack's folder has another id for the group ${v.group} than this one (${who}), so the two do not share files.`;
    default:
      return "";
  }
}

const kindWords: Record<VariableKind, string> = { env: "a setting", secret: "a secret", path: "a path", address: "an address" };

/** A refused save in words, for the row it belongs to. */
export function variableError(e: unknown, v: Pick<StackVariable, "name" | "kind" | "single">): string {
  if (!(e instanceof ProblemError)) return e instanceof Error ? e.message : String(e);
  // The daemon prefixes "NAME: "; the row already names it.
  const why = e.detail.startsWith(`${v.name}: `) ? e.detail.slice(v.name.length + 2) : e.detail;
  switch (e.code) {
    case "invalid_path":
      return `${why}. Create the folder here, or name one that exists.`;
    case "invalid_address":
      return v.single ? `${why}.` : `${why}. Use IPv4 addresses or ranges, separated by commas.`;
    case "kind_mismatch":
      return `Declared as ${kindWords[v.kind]}; it cannot be made secret.`;
    case "secret_stays_secret":
      return "A secret stays a secret. Enter a new value instead.";
    default:
      return e.detail || e.message;
  }
}

/** The words an address variable may hold for the host's networks, in the order the row shows them. */
export const ADDRESS_WORDS = [
  { word: "lan", label: "LAN" },
  { word: "vpn", label: "VPN" },
  { word: "tailnet", label: "Tailnet" },
] as const;
export type AddressWord = (typeof ADDRESS_WORDS)[number]["word"];
const isWord = (s: string): s is AddressWord => ADDRESS_WORDS.some((w) => w.word === s);

/** An address value split into its words and the other addresses, as typed. */
export function addressParts(value: string): { words: AddressWord[]; other: string } {
  const entries = value.split(/[\s,]+/).filter(Boolean);
  return { words: entries.filter(isWord), other: entries.filter((e) => !isWord(e)).join(", ") };
}

/** The address value of ticked words and other addresses: the words first, then the rest. */
export function addressValue(words: readonly AddressWord[], other: string): string {
  const ticked = ADDRESS_WORDS.map((w) => w.word).filter((w) => words.includes(w));
  return [...ticked, ...other.split(/[\s,]+/).filter(Boolean)].join(",");
}

/**
 * What the stack gets for an address value on this host, as one line: each
 * network as its CIDRs, then the custom addresses; a network that is off
 * reads "VPN off". "" for no value.
 */
export function addressPreview(value: string, networks: Schema<"StackAddressNetworks">): string {
  const { words, other } = addressParts(value);
  const nets: Record<AddressWord, string[]> = { lan: networks.lan ?? [], vpn: networks.vpn ?? [], tailnet: networks.tailnet ?? [] };
  const out = ADDRESS_WORDS.filter((w) => words.includes(w.word)).flatMap((w) => (nets[w.word].length ? nets[w.word] : [`${w.label} off`]));
  return [...out, ...other.split(/[\s,]+/).filter(Boolean)].join(" · ");
}
