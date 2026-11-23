/**
 * What the stacks pages derive from a stack and its services: its state,
 * its grants in words, and its source in both directions (what the operator
 * types, what the daemon stores).
 */
import type { ServiceInfo, Stack, StackManifest } from "./api/client";
import type { Tone } from "./components/Pill.svelte";

export interface Grant {
  kind: string;
  detail: string;
  /** Where a storage grant is bound inside the stack. */
  mount?: string;
}

const mode = (readOnly: boolean | undefined) => (readOnly ? "read-only" : "read-write");

/** Every grant a manifest asks for, worded like the daemon's grant diff. */
/** An egress destination or a port's client as the operator reads it: $NAME is an address variable, lan the host's LAN. */
export function addressWords(a: string): string {
  if (/^\$[A-Z][A-Z0-9_]{0,63}$/.test(a)) return a.slice(1);
  if (a === "lan") return "LAN";
  return a;
}

export function grants(m: StackManifest | undefined): Grant[] {
  if (!m) return [];
  const out: Grant[] = [];
  for (const r of m.routes ?? []) out.push({ kind: "route", detail: `${r.host} → :${r.port}${r.max_body ? ` · body limit ${r.max_body}` : ""}` });
  for (const s of m.storage ?? []) out.push({ kind: "storage", detail: `${s.host_path} at ${s.mount}, ${mode(s.read_only)}`, mount: s.mount });
  for (const v of m.variables ?? []) {
    if (v.kind !== "path") continue;
    const detail =
      v.default !== undefined
        ? `${v.name}: ${v.default} (default) at ${v.mount}, ${mode(v.read_only)}`
        : `${v.name} at ${v.mount}, ${mode(v.read_only)}`;
    out.push({ kind: "storage", detail, mount: v.mount });
  }
  if (m.gpu) out.push({ kind: "gpu", detail: "/dev/dri" });
  for (const e of m.egress ?? []) out.push({ kind: "egress", detail: `${addressWords(e.to)} port ${e.port}/${e.proto ?? "tcp"}` });
  for (const p of m.ports ?? [])
    out.push({
      kind: "port",
      detail: `host port ${p.port}/${p.proto ?? "tcp"} from ${(p.from ?? []).map(addressWords).join(", ")} to stack port ${p.to ?? p.port}`,
    });
  if (m.containers) out.push({ kind: "containers", detail: "podman" });
  return out;
}

/**
 * Grants the approved manifest has and the fetched one dropped. A bind the
 * fetched manifest keeps at the same mount (storage that became a path
 * variable, or a path with a new default) is not dropped.
 */
export function removedGrants(s: Stack): Grant[] {
  const next = grants(s.fetched.manifest);
  const kept = new Set(next.map((g) => g.kind + g.detail));
  const mounts = new Set(next.flatMap((g) => (g.mount ? [g.mount] : [])));
  return grants(s.approved?.manifest).filter((g) => !kept.has(g.kind + g.detail) && !(g.mount && mounts.has(g.mount)));
}

export type StackState = "running" | "frozen" | "degraded" | "stopped" | "starting" | "not-installed";

export interface Health {
  state: StackState;
  active: number;
  total: number;
  failed: ServiceInfo[];
}

export function servicesOf(all: ServiceInfo[] | undefined, stack: string): ServiceInfo[] {
  return (all ?? []).filter((s) => s.stack === stack).sort((a, b) => a.name.localeCompare(b.name));
}

/** A stack's state from its container and its service units. A disabled unit is left out: it is meant to be stopped. */
export function health(s: Stack, all: ServiceInfo[]): Health {
  const services = all.filter((u) => !u.masked);
  const active = services.filter((u) => u.active_state === "active").length;
  const failed = services.filter((u) => u.active_state === "failed");
  let state: StackState;
  if (s.generation === 0) state = "not-installed";
  else if (!s.running) state = s.want_running ? "starting" : "stopped";
  else if (s.frozen) state = "frozen";
  else if (failed.length > 0 || active < services.length) state = "degraded";
  else state = "running";
  return { state, active, total: services.length, failed };
}

/** The https links of a stack's routes. */
export function routeLinks(m: StackManifest | undefined): string[] {
  return (m?.routes ?? []).map((r) => `https://${r.host}`);
}

/**
 * The draft a stack was installed from, when its source is a repo the
 * daemon keeps for a stack written in the dashboard
 * (git+file://<state>/drafts/<name>); else null.
 */
export function draftOf(source: string): string | null {
  return source.match(/^git\+file:\/\/\/[^?]*\/homelab-daemon\/drafts\/([a-z][a-z0-9-]{0,31})(\?|$)/)?.[1] ?? null;
}

export interface SourceParts {
  repo: string;
  branch: string;
  local: boolean;
}

/** A stored source in words: github:o/r/dev → github.com/o/r, dev. */
export function describeSource(source: string): SourceParts {
  if (source.startsWith("github:")) {
    const [owner, repo, ...ref] = source.slice(7).split("/");
    return { repo: `github.com/${owner}/${repo}`, branch: ref.join("/") || "default branch", local: false };
  }
  const u = source.replace(/^git\+/, "");
  const [base, query = ""] = u.split("?");
  const ref = new URLSearchParams(query).get("ref") ?? "";
  if (base!.startsWith("file://")) return { repo: base!.slice(7), branch: ref || "checked-out branch", local: true };
  return { repo: base!, branch: ref || "default branch", local: false };
}

/** Web addresses for a stored source's repo and branch: GitHub's pages, or an https remote as is; null where there is none. */
export function sourceHref(source: string): { repo: string | null; branch: string | null } {
  if (source.startsWith("github:")) {
    const [owner, repo, ...ref] = source.slice(7).split("/");
    const base = `https://github.com/${owner}/${repo}`;
    return { repo: base, branch: ref.length ? `${base}/tree/${ref.join("/")}` : null };
  }
  const https = source.match(/^(?:git\+)?(https:\/\/[^?]+)/);
  return { repo: https ? https[1]! : null, branch: null };
}

/**
 * The source the daemon accepts for a git URL and branch. GitHub URLs, in
 * https or scp form, become github:owner/repo[/branch]; others git+https or
 * git+ssh with ?ref=. Null for something that is not a git URL.
 */
export function gitSource(url: string, branch: string): string | null {
  const u = url.trim().replace(/\/+$/, "");
  const b = branch.trim();
  const gh = u.match(/^(?:https:\/\/github\.com\/|git@github\.com:)([\w.-]+)\/([\w.-]+?)(?:\.git)?$/);
  if (gh) return `github:${gh[1]}/${gh[2]}${b ? `/${b}` : ""}`;
  const ref = b ? `?ref=${b}` : "";
  if (/^https:\/\/[\w.-]+\/[\w./~-]+$/.test(u)) return `git+${u}${ref}`;
  const scp = u.match(/^([\w.-]+)@([\w.-]+):([\w./~-]+)$/);
  if (scp) return `git+ssh://${scp[1]}@${scp[2]}/${scp[3]}${ref}`;
  if (/^ssh:\/\/[\w.@:/~-]+$/.test(u)) return `git+${u}${ref}`;
  return null;
}

/** The source for a local git checkout. Null for a relative path. */
export function folderSource(path: string): string | null {
  const p = path.trim().replace(/\/+$/, "");
  return p.startsWith("/") ? `git+file://${p}` : null;
}

/** Steps an install or update reports, matched from the daemon's phase text. */
export const updateSteps = ["Fetch", "Build", "Switch", "Health check"] as const;

export function stepOf(phase: string): number {
  if (/^building/.test(phase)) return 1;
  if (/^(switching|restarting|starting the container)/.test(phase)) return 2;
  if (/^waiting for/.test(phase)) return 3;
  if (/^running generation|^built generation|^back on/.test(phase)) return 4;
  return 0;
}

/** A service's name without its stack prefix: players-plex → plex. */
export function shortName(stack: string, unit: string): string {
  const n = unit.replace(/\.service$/, "");
  return n.startsWith(stack + "-") ? n.slice(stack.length + 1) : n;
}

/** The log viewer's id for a service unit: players-plex.service → players-plex. */
export const logId = (unit: string) => unit.replace(/\.service$/, "");

/** A stack's services as the log viewer lists them. */
export function logUnits(stack: string, services: ServiceInfo[]): { id: string; label: string }[] {
  return services.map((u) => ({ id: logId(u.name), label: shortName(stack, u.name) }));
}

/** What people see a stack called: its label, else its name. */
export const stackTitle = (s: Pick<Stack, "name" | "label">) => s.label || s.name;

/** The tone of a stack's state mark. */
export const stateTone: Record<StackState, Tone> = {
  running: "ok",
  frozen: "info",
  degraded: "warn",
  stopped: "idle",
  starting: "info",
  "not-installed": "idle",
};

/** A stack's state in one word. */
export const stateLabel: Record<StackState, string> = {
  running: "Running",
  frozen: "Frozen",
  degraded: "Degraded",
  stopped: "Stopped",
  starting: "Starting",
  "not-installed": "Not installed",
};

/** The tone of a service's state mark; a disabled service is idle. */
export function unitTone(u: ServiceInfo): Tone {
  if (u.masked) return "idle";
  if (u.active_state === "active") return "ok";
  if (u.active_state === "failed") return "bad";
  if (u.active_state === "activating" || u.active_state === "reloading") return "info";
  return "idle";
}

/** A service's state in one word: its systemd state, or disabled. */
export const unitWord = (u: ServiceInfo) => (u.masked ? "disabled" : u.active_state);

/** The manifest a stack is shown with: the approved one, else the fetched one. */
const shownManifest = (s: Stack): StackManifest | undefined => s.approved?.manifest ?? s.fetched?.manifest;

/**
 * The host's name for a unit a manifest names inside a stack (the daemon's
 * hostUnitName): plex.service in players → players-plex.service; a unit
 * that already starts with the stack's name keeps it.
 */
export const hostUnitName = (stack: string, unit: string) => (unit.startsWith(stack + "-") ? unit : `${stack}-${unit}`);

/** The address of a stack's icon, when its manifest names one. */
/**
 * The daemon's icon address. The commit is in it so that a browser, which
 * keeps an icon for a day, asks again once an update may have changed it.
 */
function iconHref(s: Stack, unit?: string): string {
  const q = new URLSearchParams();
  if (unit) q.set("unit", unit);
  const rev = s.approved?.rev ?? s.fetched?.rev;
  if (rev) q.set("rev", rev.slice(0, 12));
  return `/api/stacks/${encodeURIComponent(s.name)}/icon?${q}`;
}

export function stackIcon(s: Stack): string | undefined {
  return shownManifest(s)?.icon ? iconHref(s) : undefined;
}

/** The address of a service's icon, when the stack's manifest names one for it. */
export function unitIcon(s: Stack, u: ServiceInfo): string | undefined {
  const e = shownManifest(s)?.services?.find((m) => hostUnitName(s.name, m.unit) === u.unit_name);
  return e ? iconHref(s, e.unit) : undefined;
}

/**
 * A stack's title by name, for places that only hold the name (metrics,
 * backups, storage). A name with no stack loaded is shown as it is.
 */
export function stackTitles(stacks: Pick<Stack, "name" | "label">[] | undefined): (name: string) => string {
  const labels = new Map((stacks ?? []).filter((s) => s.label).map((s) => [s.name, s.label]));
  return (name) => labels.get(name) ?? name;
}

/** A name the daemon accepts for a stack: also its container's and its units' prefix. */
export const validStackName = (name: string) => /^[a-z][a-z0-9-]{0,31}$/.test(name);

/** The longest label the daemon accepts. */
export const MAX_STACK_LABEL = 40;

/** A nixpkgs channel or commit in words: nixos-unstable → Unstable, nixos-26.11 → 26.11. */
export function nixpkgsLabel(spec: string): string {
  if (spec === "nixos-unstable") return "Unstable";
  const release = spec.match(/^nixos-(\d\d\.\d\d)$/);
  if (release) return release[1]!;
  return /^[0-9a-f]{40}$/.test(spec) ? `Commit ${spec.slice(0, 7)}` : spec;
}

/** A full NixOS/nixpkgs commit, which the daemon accepts as a choice. */
export const isNixpkgsCommit = (s: string) => /^[0-9a-f]{40}$/.test(s);

/** The nixpkgs commit the stack's running generation was built with, if any. */
export function runningNixpkgs(s: Stack): string | undefined {
  return s.generations.find((g) => g.id === s.generation)?.nixpkgs_rev;
}

/** The update waiting is only a newer nixpkgs: the stack already runs the approved commit. */
export function nixpkgsOnly(s: Stack): boolean {
  const g = s.generations.find((g) => g.id === s.generation);
  return !!g && !!s.nixpkgs_rev && g.rev === s.approved?.rev && g.nixpkgs_rev !== s.nixpkgs_rev;
}
