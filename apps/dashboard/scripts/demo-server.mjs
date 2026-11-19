// Serves the built demo (`make demo`) for the scripts here, and stops it.
import { spawn } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const dashboard = join(dirname(fileURLToPath(import.meta.url)), "..");
export const DEMO_URL = "http://localhost:3002";

export async function serveDemo() {
  const child = spawn("npm", ["run", "preview:demo"], { cwd: dashboard, stdio: "ignore" });
  for (let i = 0; i < 60; i++) {
    const up = await fetch(DEMO_URL).then((r) => r.ok, () => false);
    if (up) return () => child.kill();
    await new Promise((r) => setTimeout(r, 250));
  }
  child.kill();
  throw new Error(`the demo did not come up at ${DEMO_URL}; run make demo first`);
}
