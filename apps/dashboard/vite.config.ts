import adapter from "@sveltejs/adapter-static";
import { sveltekit } from "@sveltejs/kit/vite";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vitest/config";

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
    }),
  ],
  server: {
    port: 3001,
    // Develop against the live daemon: /api goes to the real host.
    // The dev server sends /api to a running host: HOMELAB_URL, set by the dev shell.
    proxy: { "/api": { target: process.env.HOMELAB_URL ?? "https://homelab.example.com", changeOrigin: true, secure: false } },
  },
  resolve: process.env.VITEST ? { conditions: ["browser"] } : undefined,
  test: { environment: "jsdom", include: ["src/**/*.test.ts"] },
});
