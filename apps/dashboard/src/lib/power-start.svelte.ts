/**
 * The request that starts a reboot or poweroff, kept apart from the dialog
 * that sends it: the page moves to the power page at once and the dialog is
 * gone before the daemon answers. The power page reads the answer here.
 * Nothing survives a reload; the power page then follows the daemon alone.
 */
import { api, call, describe, ProblemError } from "#lib/api/client.ts";
import type { PowerAction } from "#lib/power.ts";

interface Start {
  /** The boot the request was sent in; the power page following it reads the rest. */
  from: string;
  pending: boolean;
  /** Why the daemon refused. */
  error: string;
  /** The install the request started, once the daemon has said which. */
  deploy: number;
}

export const start: Start = $state({ from: "", pending: false, error: "", deploy: 0 });

let sent = 0;

async function send<T>(from: string, run: () => Promise<T>): Promise<T | undefined> {
  const mine = ++sent;
  Object.assign(start, { from, pending: true, error: "", deploy: 0 });
  try {
    return await run();
  } catch (e) {
    // A shutdown already under way is the one the page follows anyway.
    if (mine === sent && !(e instanceof ProblemError && e.code === "shutting_down")) start.error = describe(e);
    return undefined;
  } finally {
    if (mine === sent) start.pending = false;
  }
}

/** thaw: frozen stacks stop cleanly; force: busy work is interrupted and frozen stacks killed. */
export function startPower(from: string, action: PowerAction, force: boolean, thaw: boolean) {
  void send(from, () => call(api.POST("/system/power", { body: { action, force, thaw } })));
}

/** Installs the downloaded update, then reboots. */
export function startInstall(from: string) {
  void send(from, () => call(api.POST("/system-updates/image/release", { body: { reboot: "now" } }))).then((d) => {
    if (d && start.from === from) start.deploy = d.id;
  });
}
