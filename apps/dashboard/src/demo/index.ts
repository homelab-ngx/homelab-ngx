/** The demo build's two stand-ins for the daemon: its API and its metrics stream. */
import { demoFetch, host } from "./fetch.ts";
import { metricsSource, type MetricsSource } from "./metrics.ts";

export { demoFetch };
export const demoMetrics = (): MetricsSource => metricsSource(host);
