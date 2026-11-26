/**
 * The daemon's API, typed from apps/core/api/openapi.json, the document the daemon emits (`make gen`).
 * Every call goes through `call`, which turns a problem+json answer into a
 * ProblemError the pages can branch on by `code`.
 */
import createClient from "openapi-fetch";
import type { components, paths } from "./schema.gen";

export type Schema<K extends keyof components["schemas"]> = components["schemas"][K];
export type Stack = Schema<"Stack">;
export type StackUpdate = Schema<"StackUpdate">;
export type StackManifest = Schema<"StackManifest">;
export type ServiceInfo = Schema<"ServiceInfo">;
export type Usage = Schema<"Usage">;
export type Draft = Schema<"Draft">;
export type DraftFile = Schema<"DraftFile">;
export type DraftChange = Schema<"DraftChange">;
export type DraftCheck = Schema<"DraftCheck">;
export type DraftBuild = Schema<"DraftBuild">;
export type DraftSummary = Schema<"DraftSummary">;
export type DraftAnswer = Schema<"DraftAnswer">;
export type AssistantSettings = Schema<"AssistantSettings">;
export type AssistantTest = Schema<"AssistantTest">;
export type AnswerUsage = Schema<"AnswerUsage">;
export type AssistantModels = Schema<"AssistantModels">;
export type AssistantLimits = Schema<"AssistantLimits">;
export type AskRequest = Schema<"AskRequest">;
export type AskAnswer = Schema<"AskAnswer">;
export type SecurityAudit = Schema<"SecurityAudit">;
export type SecurityAuditItem = Schema<"SecurityAuditItem">;
export type AuditChunk = Schema<"AuditChunk">;
export type Finding = Schema<"Finding">;
export type StackSSHKey = Schema<"StackSSHKey">;
export type StackSSHKeys = Schema<"StackSSHKeys">;

/**
 * The demo build (`vite build --mode demo`) answers every call in the browser
 * from a made-up host (src/demo). Any other build has no such code: the
 * condition is a constant, so the import is dropped.
 */
export const DEMO = import.meta.env.MODE === "demo";
const demo = DEMO ? import("../../demo/index.ts") : undefined;

export const api = createClient<paths>({
  baseUrl: "/api",
  fetch: demo ? async (request) => (await demo).demoFetch(request) : undefined,
});

/** An RFC 9457 answer from the daemon. */
export class ProblemError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    readonly detail: string,
    readonly extra: Record<string, unknown> = {},
  ) {
    super(detail || `HTTP ${status}`);
    this.name = "ProblemError";
  }
}

type Result<T> = { data?: T; error?: unknown; response: Response };

/** Why the daemon wants the browser to sign in. */
export type SignInNeeded = "setup_required" | "login_required";

let signedOut: ((code: SignInNeeded) => void) | null = null;

/** Called whenever an answer says the session is gone or no password is set yet. */
export function onSignedOut(fn: (code: SignInNeeded) => void) {
  signedOut = fn;
}

/** The answer's body, or a ProblemError. */
export async function call<T>(request: Promise<Result<T>>): Promise<T> {
  const { data, error, response } = await request;
  if (error !== undefined || !response.ok) {
    const p = (error ?? {}) as { code?: string; detail?: string; title?: string };
    const code = p.code ?? "";
    if (response.status === 401 && (code === "setup_required" || code === "login_required")) signedOut?.(code);
    const extra = error && typeof error === "object" ? (error as Record<string, unknown>) : {};
    throw new ProblemError(response.status, code, p.detail ?? p.title ?? response.statusText, extra);
  }
  return data as T;
}

/** What to show for a failed call. */
export function describe(error: unknown): string {
  if (error instanceof ProblemError) return error.detail;
  if (error instanceof TypeError) return "The daemon could not be reached.";
  return error instanceof Error ? error.message : String(error);
}
