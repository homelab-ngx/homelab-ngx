/**
 * The host's clock format (System Settings › General), for every time the
 * dashboard shows. The shell sets it from the host's settings; "" follows
 * the browser's language.
 */
import type { Schema } from "./api/client.ts";

export type Clock = Schema<"Time">["clock"];

export const clock = $state<{ format: Clock }>({ format: "" });
