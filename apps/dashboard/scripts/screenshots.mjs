// The README's screenshots, into docs/images: each page in the light and the
// dark theme, 1366x768. Run it through `make screenshots` (the dashboard dev
// shell brings the browser):
//
//   make screenshots HOMELAB_URL=https://host.example HOMELAB_PASSWORD=...
//       the pages of a running host, signed in with its dashboard password
//       STACK=name            the stack whose page is shown (default: the first listed)
//       INSTALLER_URL=http:// an installer stick's page, for the installer shot
//   make screenshots PLACEHOLDERS=1
//       grey panels with each page's name, for a page nobody has captured yet
//
// A screenshot shows the host's names and addresses. Look at each file
// before committing it: the commit checks read text, not images.
import { mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";

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

async function placeholders(browser) {
  // The dashboard's own colours (src/app.css).
  const colours = { light: ["#f4f5f7", "#5d6773", "#0f6e8c"], dark: ["#101317", "#8e99a6", "#5cb8d6"] };
  const page = await browser.newPage({ viewport: size });
  for (const [name] of [...pages, ["installer"]]) {
    for (const theme of themes) {
      const [bg, muted, accent] = colours[theme];
      await page.setContent(`<body style="margin:0;height:100vh;display:grid;place-content:center;text-align:center;
        background:${bg};color:${muted};font:20px system-ui,sans-serif">
        <div style="font-size:44px;font-weight:600;color:${accent}">${name}</div>
        <div style="margin-top:12px">Screenshot to come &middot; ${size.width}&times;${size.height} &middot; ${theme}</div></body>`);
      await page.screenshot({ path: join(out, `${name}-${theme}.png`) });
    }
  }
}

async function shoot(page, name, theme) {
  // Charts and lists fill in after the first paint.
  await page.waitForLoadState("networkidle");
  await page.screenshot({ path: join(out, `${name}-${theme}.png`) });
  console.log(`${name}-${theme}.png`);
}

async function capture(browser) {
  const base = (env.HOMELAB_URL ?? "").replace(/\/$/, "");
  if (!base || !env.HOMELAB_PASSWORD) fail("set HOMELAB_URL and HOMELAB_PASSWORD, or PLACEHOLDERS=1");
  for (const theme of themes) {
    // A host's own certificate is often private; this only reads pages.
    const context = await browser.newContext({ viewport: size, ignoreHTTPSErrors: true, colorScheme: theme });
    await context.addInitScript((t) => localStorage.setItem("theme", t), theme);
    const page = await context.newPage();
    await page.goto(base + "/");
    const password = page.locator('input[type="password"]');
    await password.first().waitFor({ timeout: 15000 }).catch(() => {});
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
        target = env.STACK
          ? `/stacks/${env.STACK}`
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

    if (env.INSTALLER_URL) {
      const installer = await browser.newContext({ viewport: size, ignoreHTTPSErrors: true, colorScheme: theme });
      await installer.addInitScript((t) => localStorage.setItem("theme", t), theme);
      const page = await installer.newPage();
      await page.goto(env.INSTALLER_URL);
      await shoot(page, "installer", theme);
      await installer.close();
    }
  }
}

await mkdir(out, { recursive: true });
const browser = await chromium.launch();
try {
  await (env.PLACEHOLDERS ? placeholders(browser) : capture(browser));
} finally {
  await browser.close();
}
