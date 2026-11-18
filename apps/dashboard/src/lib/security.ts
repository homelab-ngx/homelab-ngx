import {
  api,
  call,
  type Finding,
  type SecurityAudit,
  type SecurityAuditItem,
} from "#lib/api/client.ts";
import type { Tone } from "#lib/components/Pill.svelte";

export const securityKeys = {
  audits: ["daemon", "security", "audits"] as const,
  audit: (id: number) => ["daemon", "security", "audits", id] as const,
};

export const fetchAudits = () =>
  call<SecurityAuditItem[]>(api.GET("/security/audits"));
export const fetchAudit = (id: number) =>
  call<SecurityAudit>(
    api.GET("/security/audits/{id}", { params: { path: { id } } }),
  );

export const SEVERITY: Record<Finding["severity"], Tone> = {
  critical: "bad",
  high: "bad",
  medium: "warn",
  low: "info",
  info: "idle",
};

export const POSTURE: Record<
  NonNullable<SecurityAuditItem["posture"]>,
  Tone
> = {
  good: "ok",
  fair: "info",
  weak: "warn",
  poor: "bad",
};

export const RESULT: Record<
  SecurityAuditItem["result"],
  { tone: Tone; label: string }
> = {
  running: { tone: "info", label: "running" },
  done: { tone: "ok", label: "done" },
  partial: { tone: "warn", label: "partial" },
  failed: { tone: "bad", label: "failed" },
};

/** The count of findings at each severity, most severe first, without the empty ones. */
export function countsLabel(c: SecurityAuditItem["counts"]): string {
  const parts = (["critical", "high", "medium", "low"] as const)
    .filter((k) => c[k] > 0)
    .map((k) => `${c[k]} ${k}`);
  return parts.length ? parts.join(" · ") : "no findings";
}

/** What an audit cost, shortest form: dollars charged, else the plan's allowance spent, else tokens. */
export function costLabel(u: SecurityAuditItem["usage"]): string {
  const dollars = (n: number) =>
    `$${n >= 0.01 ? n.toFixed(2) : n.toPrecision(2)}`;
  if (u.cost) return dollars(u.cost);
  if (u.allowance) return `${dollars(u.allowance)} of plan`;
  const tokens = u.input_tokens + u.output_tokens;
  return tokens ? `${Math.round(tokens / 1000)}k tokens` : "–";
}

/** Every finding of an audit by id. */
export function findingsById(a: SecurityAudit): Map<string, Finding> {
  return new Map(a.chunks.flatMap((c) => c.findings).map((f) => [f.id, f]));
}
