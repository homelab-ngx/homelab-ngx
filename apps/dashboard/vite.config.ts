import { createHash } from "node:crypto";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import adapter from "@sveltejs/adapter-static";
import { sveltekit } from "@sveltejs/kit/vite";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vitest/config";

/** Deterministic SvelteKit version name so two builds of the same commit match. */
function buildVersion(): string {
  const root = fileURLToPath(new URL(".", import.meta.url));
  const h = createHash("sha256");
  const walk = (rel: string): void => {
    for (const ent of readdirSync(`${root}/${rel}`, { withFileTypes: true }).sort((a, b) =>
      a.name < b.name ? -1 : a.name > b.name ? 1 : 0,
    )) {
      const next = `${rel}/${ent.name}`;
      if (ent.isDirectory()) {
        walk(next);
      } else if (ent.isFile()) {
        h.update(next);
        h.update("\0");
        h.update(readFileSync(`${root}/${next}`));
        h.update("\0");
      }
    }
  };
  walk("src");
  walk("static");
  h.update("package-lock.json\0");
  h.update(readFileSync(`${root}/package-lock.json`));
  return h.digest("hex").slice(0, 16);
}

export default defineConfig({
  plugins: [
    tailwindcss(),
    sveltekit({
      // A single-page app: every route renders in the browser from the
      // index.html fallback, which Caddy serves for unknown paths.
      adapter: adapter({ fallback: "index.html", precompress: true }),
      // Runes only: Svelte 4 syntax fails the build instead of shipping.
      compilerOptions: { runes: true },
      // static/sw.js is registered by the layout; kit does not build one.
      serviceWorker: { register: false },
      // Derived from the dashboard source tree instead of Date.now() so builds are reproducible.
      version: { name: buildVersion() },
    }),
  ],
  server: {
    port: 3001,
    // Develop against the live daemon: /api goes to the real host.
    // The dev server sends /api to a running host: HOMELAB_URL, set by the dev shell.
    proxy: { "/api": { target: process.env.HOMELAB_URL ?? "https://homelab.example.com", changeOrigin: true, secure: false, ws: true } },
  },
  resolve: process.env.VITEST ? { conditions: ["browser"] } : undefined,
  test: { environment: "jsdom", include: ["src/**/*.test.ts"] },
});
