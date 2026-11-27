import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "#lib/api/client.ts";
import { SEEN_VERSION_KEY } from "./changelog.ts";
import type { ImageStatus } from "./core.ts";
import Harness from "./UpdatedDialogHarness.test.svelte";

const nav = vi.hoisted(() => ({ replaceState: vi.fn() }));
const state = vi.hoisted(() => ({ page: { url: new URL("http://localhost/") } }));

vi.mock("$app/navigation", () => nav);
vi.mock("$app/state", () => state);

const img = (booted: string): ImageStatus => ({
  booted,
  installed: [
    { version: booted, state: "booted" },
    { version: "0.2.2", state: "good" },
  ],
  available: [],
  pending_reboot: false,
  core: { active: booted, installed: [booted], available: [] },
  source: "release",
  release: { url: "", latest: booted, checked_at: null },
});

const changelogBody = {
  releases: [
    {
      version: "0.2.6",
      date: "2026-11-26",
      changes: [{ area: "host", text: "add outbound SMTP relay" }],
      url: "https://github.com/homelab-ngx/homelab-ngx/releases/tag/v0.2.6",
    },
    {
      version: "0.2.5",
      date: "2026-11-24",
      changes: [{ area: "backups", text: "preview backups before copying" }],
    },
  ],
};

function mockAPI(opts: { running: string; versionEndpoint?: string }) {
  return vi.spyOn(api, "GET").mockImplementation((path: string) => {
    if (path === "/system-updates/image") {
      return Promise.resolve({ data: img(opts.running), response: new Response(null, { status: 200 }) }) as never;
    }
    if (path === "/system-updates/changelog") {
      return Promise.resolve({ data: changelogBody, response: new Response(null, { status: 200 }) }) as never;
    }
    if (path === "/version") {
      return Promise.resolve({
        data: { version: opts.versionEndpoint ?? opts.running, commit: "abc" },
        response: new Response(null, { status: 200 }),
      }) as never;
    }
    throw new Error(`unexpected GET ${path}`);
  });
}

beforeEach(() => {
  localStorage.clear();
  state.page.url = new URL("http://localhost/");
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("UpdatedDialog", () => {
  it("shows single-version changelog after an explicit update", async () => {
    state.page.url = new URL("http://localhost/?updated=0.2.6&from=0.2.5");
    mockAPI({ running: "0.2.6" });
    render(Harness, { props: { client: new QueryClient({ defaultOptions: { queries: { retry: false } } }) } });

    expect(await screen.findByText("add outbound SMTP relay")).toBeTruthy();
    expect(screen.getByText("from 0.2.5")).toBeTruthy();
    expect(screen.getByText("What changed")).toBeTruthy();
  });

  it("shows all intermediate releases when skipping a version", async () => {
    state.page.url = new URL("http://localhost/?updated=0.2.6&from=0.2.2");
    mockAPI({ running: "0.2.6" });
    render(Harness, { props: { client: new QueryClient({ defaultOptions: { queries: { retry: false } } }) } });

    expect(await screen.findByText("add outbound SMTP relay")).toBeTruthy();
    expect(screen.getByText("preview backups before copying")).toBeTruthy();
  });

  it("hides the target changelog when the boot went back", async () => {
    state.page.url = new URL("http://localhost/?updated=0.2.6&from=0.2.5");
    mockAPI({ running: "0.2.5" });
    render(Harness, { props: { client: new QueryClient({ defaultOptions: { queries: { retry: false } } }) } });

    expect(await screen.findByText("Update went back")).toBeTruthy();
    expect(screen.getByText("0.2.6 did not start.")).toBeTruthy();
    expect(screen.queryByText("add outbound SMTP relay")).toBeNull();
  });

  it("opens automatically on a newer release than seen before and records it on close", async () => {
    localStorage.setItem(SEEN_VERSION_KEY, "0.2.5");
    mockAPI({ running: "0.2.6" });
    render(Harness, { props: { client: new QueryClient({ defaultOptions: { queries: { retry: false } } }) } });

    expect(await screen.findByText("add outbound SMTP relay")).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(localStorage.getItem(SEEN_VERSION_KEY)).toBe("0.2.6");
  });

  it("stays silent on first visit and records the current release", async () => {
    mockAPI({ running: "0.2.6" });
    render(Harness, { props: { client: new QueryClient({ defaultOptions: { queries: { retry: false } } }) } });

    await waitFor(() => expect(localStorage.getItem(SEEN_VERSION_KEY)).toBe("0.2.6"));
    expect(screen.queryByText("Updated")).toBeNull();
  });
});
