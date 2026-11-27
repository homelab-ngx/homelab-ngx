import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "#lib/api/client.ts";
import type { CoreDeploy, ImageStatus } from "#lib/components/core/core.ts";

vi.hoisted(() => {
  if (typeof window !== "undefined" && !window.matchMedia) {
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: () => ({
        matches: false,
        media: "",
        onchange: null,
        addListener: () => {},
        removeListener: () => {},
        addEventListener: () => {},
        removeEventListener: () => {},
        dispatchEvent: () => false,
      }),
    });
  }
});

import Harness from "./UpdatesPageHarness.test.svelte";

const nowIso = () => new Date().toISOString();

const image: ImageStatus = {
  booted: "0.2.2",
  installed: [
    { version: "0.2.2", state: "booted" },
    { version: "0.2.1", state: "good" },
  ],
  available: [],
  pending_reboot: false,
  core: { active: "0.2.2", installed: ["0.2.2"], available: [] },
  source: "github:example/example",
  release: { url: "", latest: "0.2.2", checked_at: nowIso() },
};

const deploy = (over: Partial<CoreDeploy>): CoreDeploy => ({
  id: 1,
  kind: "download",
  rev: "",
  version: "0.2.2",
  phase: "Up to date (0.2.2)",
  result: "healthy",
  reason: "",
  failed_units: [],
  started_at: nowIso(),
  finished_at: nowIso(),
  log: "",
  ...over,
});

function mockApi(deploys: CoreDeploy[], onPostDownload?: () => CoreDeploy) {
  const get = vi.spyOn(api, "GET").mockImplementation(((path: string) => {
    const ok = (data: unknown) => Promise.resolve({ data, response: new Response(null, { status: 200 }) });
    if (path === "/system-updates/deploys") return ok(deploys);
    if (path === "/system-updates/image") return ok(image);
    if (path === "/system-updates/changelog") return ok({ releases: [] });
    if (path === "/settings/assistant") return ok({ configured: false });
    if (path === "/system/power") {
      return ok({ phase: "idle", steps: [], busy: [], frozen: [], boot_id: "b1", expected_seconds: 90 });
    }
    return ok(undefined);
  }) as never);
  const post = vi.spyOn(api, "POST").mockImplementation(((path: string) => {
    if (path === "/system-updates/image/download" && onPostDownload) {
      return Promise.resolve({ data: onPostDownload(), response: new Response(null, { status: 200 }) });
    }
    return Promise.resolve({ data: undefined, response: new Response(null, { status: 200 }) });
  }) as never);
  return () => {
    get.mockRestore();
    post.mockRestore();
  };
}

const renderPage = () =>
  render(Harness, {
    props: {
      client: new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    },
  });

afterEach(cleanup);

describe("Updates page", () => {
  it("reverts to the normal state after a healthy deploy instead of showing DeployCard", async () => {
    const restore = mockApi([deploy({ id: 5, kind: "build", phase: "Prepared 0.2.2", result: "healthy" })]);
    try {
      renderPage();
      await vi.waitFor(() => expect(screen.getByRole("button", { name: "Check now" })).toBeTruthy());
      expect(document.body.textContent).not.toContain("Building from main");
      expect(document.body.textContent).not.toContain("Verify signature");
      expect(document.body.textContent).not.toContain("Image is up to date.");
    } finally {
      restore();
    }
  });

  it("shows a quiet inline notice without step bars when Check now is up to date", async () => {
    const instant = deploy({ id: 9, kind: "download", phase: "Up to date (0.2.2)", result: "healthy" });
    const restore = mockApi([], () => instant);
    try {
      renderPage();
      const checkBtn = await vi.waitFor(() => screen.getByRole("button", { name: "Check now" }));
      await fireEvent.click(checkBtn);
      await vi.waitFor(() => expect(document.body.textContent).toContain("Image is up to date."));
      expect(document.body.textContent).not.toContain("Verify signature");

      await fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
      expect(document.body.textContent).not.toContain("Image is up to date.");
    } finally {
      restore();
    }
  });

  it("shows a dismissible DeployCard for a recent failure", async () => {
    const restore = mockApi([
      deploy({ id: 12, kind: "build", phase: "Build", result: "build_failed", reason: "build error" }),
    ]);
    try {
      renderPage();
      await vi.waitFor(() => expect(document.body.textContent).toContain("Building from main"));
      expect(document.body.textContent).toContain("build error");

      await fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
      expect(document.body.textContent).not.toContain("Building from main");
    } finally {
      restore();
    }
  });
});
