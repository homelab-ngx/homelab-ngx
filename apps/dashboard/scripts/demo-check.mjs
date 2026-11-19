// Loads every page of the built demo in a browser and fails on a console
// error, a failed request or an API answer the page did not expect. Run it
// through `make demo-check`.
import { chromium } from "playwright-core";
import { DEMO_URL, serveDemo } from "./demo-server.mjs";

const pages = [
  "/", "/stacks", "/stacks/photos", "/stacks/home", "/stacks/add", "/storage", "/storage/browse", "/storage/browse?path=/media",
  "/backups", "/backups/1", "/notifications", "/settings", "/settings/network", "/settings/updates", "/settings/security",
  "/settings/assistant", "/settings/logs", "/?installer",
];

const stop = await serveDemo();
const browser = await chromium.launch();
const problems = [];
try {
  const page = await browser.newPage({ viewport: { width: 1366, height: 768 } });
  let at = "";
  page.on("console", (m) => m.type() === "error" && problems.push(`${at}: console: ${m.text()}`));
  page.on("pageerror", (e) => problems.push(`${at}: ${e.message}`));
  page.on("requestfailed", (r) => problems.push(`${at}: request failed: ${r.url()}`));
  page.on("response", (r) => r.status() >= 400 && problems.push(`${at}: HTTP ${r.status()} ${r.url()}`));
  for (at of pages) {
    await page.goto(DEMO_URL + at);
    await page.waitForLoadState("networkidle");
    // Long enough for the first poll and the first live sample.
    await page.waitForTimeout(2500);
    const text = (await page.locator("main, body").first().innerText()).trim();
    if (text.length < 40) problems.push(`${at}: the page is empty`);
    if (!at.includes("installer") && !(await page.getByText("Demo. Dummy data").count())) problems.push(`${at}: the demo notice is missing`);
    console.log(`${at}: ${text.length} characters`);
  }
} finally {
  await browser.close();
  stop();
}
if (problems.length) {
  console.error(problems.join("\n"));
  process.exit(1);
}
console.log(`demo-check: ${pages.length} pages, no errors`);
