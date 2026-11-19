import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
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
  release: { url: "", latest: "0.2.2", checked_at: null },
  ...over,
});
const show = (image: ImageStatus) => render(Harness, { props: { client: new QueryClient(), image } });
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
  it("uses the page's words for a version's state", () => {
    show(img());
    expect(text()).toContain("up to date");
    expect(text()).toContain("fallback");
    expect(text()).toContain("running");
    expect(text()).not.toMatch(/\bgood\b|staged|Install/);
    expect(screen.queryByRole("button", { name: /^Update to/ })).toBeNull();
  });
  it("asks for the reboot when an update waits for one", () => {
    show(img({ pending_reboot: true, installed: [{ version: "0.2.2", state: "pending" }, { version: "0.0.202611182048", state: "booted" }] }));
    expect(text()).toContain("reboot to finish");
    expect(text()).toContain("next boot");
    expect(screen.getByRole("button", { name: "Reboot…" })).toBeTruthy();
  });
  it("says a version that did not start, outside the fold", () => {
    show(img({ fell_back_from: "0.2.2", installed: [{ version: "0.2.2", state: "bad" }, { version: "0.0.202611182048", state: "booted" }] }));
    expect(text()).toContain("0.2.2 did not start");
  });
  it("hides the build without a source", () => {
    show(img({ source: "" }));
    expect(screen.queryByRole("button", { name: "Build from main" })).toBeNull();
  });
});
