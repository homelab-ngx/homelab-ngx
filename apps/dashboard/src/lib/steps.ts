/**
 * Steps shown as rows with a bar each: the stops of a shutdown, the steps
 * of an update. A row's bar says the state as the pill beside it does.
 */
import type { Tone } from "#lib/components/Pill.svelte";

/** How a step's own bar looks: empty, running, full, or full in a warning or failure colour. */
export type Bar = "empty" | "run" | "full" | "warn" | "bad" | "unknown";

/** One row of the step list: a stack, a mount, or a step of the install. */
export interface Row {
  key: string;
  name: string;
  tone: Tone;
  word: string;
  bar: Bar;
  /** What it waits for, and when systemd kills it. */
  wait?: { what: string; when: string };
  detail?: string;
}
