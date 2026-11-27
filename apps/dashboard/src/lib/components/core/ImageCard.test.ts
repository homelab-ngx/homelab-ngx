import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "#lib/api/client.ts";
import type { ImageStatus } from "./core.ts";
import Harness from "./ImageCardHarness.test.svelte";

const img = (over: Partial<ImageStatus> = {}): ImageStatus => ({
  booted: "0.0.202611182048",
  installed: [
    { version: "0.0.202611182048", state: "booted" },
    { version: "0.0.202611181531", state: "good" },
  ],
  available: [],
  pending_reboot: false,
  core: { active: "0.0.202611182129", installed: [], available: [] },
  source: "github:example/example",
  release: { url: "", latest: "0.0.202611182129", checked_at: null },
  ...over,
});
const show = (image: ImageStatus, onStarted?: (d: import("./core.ts").CoreDeploy) => void) =>
  render(Harness, { props: { client: new QueryClient(), image, onStarted } });
const text = () => document.body.textContent ?? "";

afterEach(cleanup);

describe("ImageCard", () => {
  it("offers the newest download as one update and lists no older one", () => {
    show(img({ available: ["0.2.2", "0.1.0"], core: { active: "0.0.202611182129", installed: [], available: ["0.2.2", "0.1.0"] } }));
    expect(screen.getByRole("button", { name: "Update to 0.2.2…" })).toBeTruthy();
    expect(text()).toContain("update ready");
    expect(text()).toContain("needs a reboot");
    expect(text()).not.toContain("0.1.0");
  });
  it("labels installed images by role and omits the normal count", () => {
    show(img());
    expect(text()).toContain("up to date");
    expect(text()).toContain("Installed images");
    expect(text()).toContain("Running image");
    expect(text()).toContain("Fallback image");
    expect(text()).not.toContain("2 installed");
    expect(text()).not.toMatch(/\bgood\b|staged|Install\b/);
    expect(screen.queryByRole("button", { name: /^Update to/ })).toBeNull();
  });
  it("asks for the reboot when an update waits for one", () => {
    show(img({ pending_reboot: true, installed: [{ version: "0.2.2", state: "pending" }, { version: "0.0.202611182048", state: "booted" }] }));
    expect(text()).toContain("reboot to finish");
    expect(text()).toContain("Next boot");
    expect(screen.getByRole("button", { name: "Reboot…" })).toBeTruthy();
  });
  it("says a version that did not start, outside the fold", () => {
    show(img({ fell_back_from: "0.2.2", installed: [{ version: "0.2.2", state: "bad" }, { version: "0.0.202611182048", state: "booted" }] }));
    expect(text()).toContain("0.2.2 did not start");
  });
  it("offers to download when a newer release exists on the server", () => {
    show(img({ release: { url: "", latest: "0.2.2", checked_at: null } }));
    expect(text()).toContain("update available");
    expect(screen.getByRole("button", { name: "Download 0.2.2" })).toBeTruthy();
  });
  it("hides the build without a source", () => {
    show(img({ source: "" }));
    expect(screen.queryByRole("button", { name: "Build from main" })).toBeNull();
  });
  it("reports the returned deploy to onStarted when Check now runs", async () => {
    const returned = {
      id: 42,
      kind: "download",
      rev: "",
      version: "0.2.2",
      phase: "Up to date (0.2.2)",
      result: "healthy",
      reason: "",
      failed_units: [],
      started_at: "2026-11-27T10:00:00Z",
      finished_at: "2026-11-27T10:00:00Z",
      log: "",
    };
    const post = vi.spyOn(api, "POST").mockResolvedValue({
      data: returned,
      response: new Response(null, { status: 200 }),
    } as never);
    const onStarted = vi.fn();
    try {
      show(img(), onStarted);
      await fireEvent.click(screen.getByRole("button", { name: "Check now" }));
      await vi.waitFor(() => expect(onStarted).toHaveBeenCalledWith(returned));
    } finally {
      post.mockRestore();
    }
  });
  it("warns when rebooting now would interrupt a running backup", async () => {
    const get = vi.spyOn(api, "GET").mockResolvedValue({
      data: { phase: "idle", steps: [], busy: ["backup All stacks except Immich"], frozen: [], boot_id: "b1", expected_seconds: 90 },
      response: new Response(null, { status: 200 }),
    } as never);
    try {
      show(img({ available: ["0.2.2"], core: { active: "0.0.202611182129", installed: [], available: ["0.2.2"] } }));
      await fireEvent.click(screen.getByRole("button", { name: "Update to 0.2.2…" }));
      expect(text()).not.toContain("Interrupted:");
      expect(screen.getByRole("button", { name: "Update" })).toBeTruthy();

      await fireEvent.click(screen.getByLabelText("Now"));
      await vi.waitFor(() => expect(text()).toContain("backup All stacks except Immich"));
      expect(text()).toContain("Interrupted:");
      expect(screen.getByRole("button", { name: "Update anyway" })).toBeTruthy();

      await fireEvent.click(screen.getByLabelText("At"));
      expect(text()).not.toContain("Interrupted:");
      expect(screen.getByRole("button", { name: "Update" })).toBeTruthy();
    } finally {
      get.mockRestore();
    }
  });
});
