/** Log export helpers shared across stack and host log views. */
import { segments } from "./ansi.ts";
import { stamp } from "./format.ts";

export interface LogLine {
  time: string;
  priority: number;
  message: string;
}

/** Highest syslog priority each level keeps. */
export const LOG_LEVELS = [
  { id: "", label: "All levels", priority: undefined },
  { id: "error", label: "Errors", priority: 3 },
  { id: "warn", label: "Warnings and errors", priority: 4 },
  { id: "info", label: "Info and up", priority: 6 },
] as const;

export function levelWord(p: number): "ERROR" | "WARN" | "DEBUG" | "INFO" {
  if (p <= 3) return "ERROR";
  if (p === 4) return "WARN";
  if (p === 7) return "DEBUG";
  return "INFO";
}

/** Formats visible log entries as plain text with ANSI escapes stripped. */
export function formatLogLines(lines: readonly LogLine[]): string {
  return lines
    .map((l) => `${stamp(l.time)} ${levelWord(l.priority)} ${segments(l.message).map((s) => s.text).join("")}`)
    .join("\n");
}
