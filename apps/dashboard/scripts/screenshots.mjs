// The README's screenshots, into docs/images: each page in the light and the
// dark theme, 1366x768. Run it through `make screenshots` (the dashboard dev
// shell brings the browser):
//
//   make screenshots
//       from the demo (src/demo), which the target builds first: a made-up
//       host, so nothing of a real one is in a picture
//   make screenshots HOMELAB_URL=https://host.example HOMELAB_PASSWORD=...
//       from a running host instead, signed in with its dashboard password
//       STACK=name            the stack whose page is shown (default: the first listed)
//       INSTALLER_URL=http:// an installer stick's page, for the installer shot
//
// A screenshot of a real host shows its names and addresses. Look at each
// file before committing it: the commit checks read text, not images.
import { mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";
import { DEMO_URL, serveDemo } from "./demo-server.mjs";

const out = join(dirname(fileURLToPath(import.meta.url)), "../../../docs/images");
const size = { width: 1366, height: 768 };
const themes = ["light", "dark"];
const pages = [
  ["overview", "/"],
  ["stacks", "/stacks"],
  ["stack", null],
  ["storage", "/storage"],
  ["backups", "/backups"],
];
const env = process.env;

function fail(message) {
  console.error(`screenshots: ${message}`);
  process.exit(1);
}

async function shoot(page, name, theme) {
  // Charts and lists fill in after the first paint.
  await page.waitForLoadState("networkidle");
  await page.waitForTimeout(1500);
  await page.screenshot({ path: join(out, `${name}-${theme}.png`) });
  console.log(`${name}-${theme}.png`);
}

async function capture(browser) {
  const demo = !env.SCREENSHOT_HOST;
  const base = demo ? DEMO_URL : env.SCREENSHOT_HOST.replace(/\/$/, "");
  const installerUrl = demo ? `${DEMO_URL}/?installer` : env.INSTALLER_URL;
  if (!demo && !env.HOMELAB_PASSWORD) fail("set HOMELAB_PASSWORD beside HOMELAB_URL");
  for (const theme of themes) {
    // A host's own certificate is often private; this only reads pages.
    const context = await browser.newContext({ viewport: size, ignoreHTTPSErrors: true, colorScheme: theme });
    await context.addInitScript((t) => localStorage.setItem("theme", t), theme);
    const page = await context.newPage();
    await page.goto(base + "/");
    const password = page.locator('input[type="password"]');
    if (!demo) await password.first().waitFor({ timeout: 15000 }).catch(() => {});
    if ((await password.count()) > 1) fail(`${base} has no password yet: set one in the dashboard first`);
    if ((await password.count()) === 1) {
      await password.fill(env.HOMELAB_PASSWORD);
      await password.press("Enter");
      await password.waitFor({ state: "detached", timeout: 15000 }).catch(() => fail("the password was not accepted"));
    }
    for (const [name, path] of pages) {
      let target = path;
      if (name === "stack") {
        await page.goto(base + "/stacks");
        await page.waitForLoadState("networkidle");
        target = env.STACK || demo
          ? `/stacks/${env.STACK || "photos"}`
          : await page.locator('a[href^="/stacks/"]:not([href="/stacks/add"]):not([href^="/stacks/drafts"])').first().getAttribute("href").catch(() => null);
        if (!target) {
          console.log("stack: no stack on this host, skipped");
          continue;
        }
      }
      await page.goto(base + target);
      await shoot(page, name, theme);
    }
    await context.close();

    if (installerUrl) {
      const installer = await browser.newContext({ viewport: size, ignoreHTTPSErrors: true, colorScheme: theme });
      await installer.addInitScript((t) => localStorage.setItem("theme", t), theme);
      const page = await installer.newPage();
      await page.goto(installerUrl);
      await shoot(page, "installer", theme);
      await installer.close();
    }
  }
}

await mkdir(out, { recursive: true });
const stop = env.SCREENSHOT_HOST ? () => {} : await serveDemo();
const browser = await chromium.launch();
try {
  await capture(browser);
} finally {
  await browser.close();
  stop();
}
