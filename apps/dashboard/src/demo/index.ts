/** The demo build's stand-ins for the daemon: its API and its live streams. */
import type { NotificationStreamSource } from "#lib/live-notifications.svelte.ts";
import { demoFetch, host } from "./fetch.ts";
import { metricsSource, type MetricsSource } from "./metrics.ts";
import { notificationsSource } from "./notifications.ts";

export { demoFetch };
export const demoMetrics = (): MetricsSource => metricsSource(host);
export const demoNotifications = (): NotificationStreamSource => notificationsSource(host);
