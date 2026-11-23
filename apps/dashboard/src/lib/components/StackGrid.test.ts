import { cleanup, render, screen, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
import type { ServiceInfo, Stack } from "../api/client.ts";
import StackGrid from "./StackGrid.svelte";

const unit = (name: string, active_state: string, stack = "media"): ServiceInfo =>
  ({ name, stack, active_state, sub_state: "", type: "Native", description: "", unit_name: name + ".service", started_at: null }) as ServiceInfo;

const manifest = (icon?: string) => ({ version: 9, name: "x", routes: [], storage: [], egress: [], gpu: false, health: { units: [] }, ...(icon ? { icon } : {}) });
const stack = (name: string, extra: Partial<Stack> = {}, icon?: string): Stack =>
  ({
    name,
    source: "github:o/" + name,
    running: true,
    want_running: true,
    generation: 2,
    generations: [{ id: 2, rev: "abcdef0123", built_at: "", failed: false }],
    fetched: { rev: "abcdef0123", url: "", at: "", manifest: manifest(icon) },
    approved: { rev: "abcdef0123", url: "", at: "", manifest: manifest(icon) },
    update_state: "up_to_date",
    commits: [],
    new_grants: [],
    variables_ready: true,
    restart_needed: false,
    idle_grants: [],
    variables: [],
    ...extra,
  }) as unknown as Stack;

const card = (name: string) => screen.getByRole("link", { name: new RegExp(name) });

describe("StackGrid", () => {
  afterEach(cleanup);

  it("links each card to its stack, titled by its label, with its icon", () => {
    const { container } = render(StackGrid, { props: { stacks: [stack("media", { label: "Movies & TV" }, "plex")], services: [], view: "minimal" } });
    expect(card("Movies & TV").getAttribute("href")).toBe("/stacks/media");
    expect(within(card("Movies & TV")).getByText("media")).toBeTruthy();
    expect(container.querySelector("img")?.getAttribute("src")).toBe("/api/stacks/media/icon?rev=abcdef0123");
  });

  it("marks a failed service in the minimal strip and counts it", () => {
    render(StackGrid, { props: { stacks: [stack("media")], services: [unit("media-plex", "active"), unit("media-sonarr", "failed")], view: "minimal" } });
    expect(within(card("media")).getByText(/degraded/i)).toBeTruthy();
    expect(within(card("media")).getByText("1/2")).toBeTruthy();
    expect(within(card("media")).getByText("sonarr: failed")).toBeTruthy();
    // Minimal shows the services as icons, not rows.
    expect(within(card("media")).queryByText("plex")).toBeNull();
  });

  it("leaves a disabled service out of the count and shows it as disabled in detail", () => {
    const services = [unit("media-plex", "active"), { ...unit("media-sonarr", "inactive"), masked: true }];
    render(StackGrid, { props: { stacks: [stack("media")], services, view: "detailed" } });
    expect(within(card("media")).getByText(/running/i)).toBeTruthy();
    expect(within(card("media")).queryByText("1/2")).toBeNull();
    expect(within(card("media")).getByText("sonarr")).toBeTruthy();
    expect(within(card("media")).getByText("disabled")).toBeTruthy();
  });

  it("says when an update is ready and when one needs approval", () => {
    const stacks = [stack("email", { update_state: "commits_available" } as Partial<Stack>), stack("media", { update_state: "needs_approval" } as Partial<Stack>)];
    render(StackGrid, { props: { stacks, services: [], view: "minimal" } });
    expect(within(card("email")).getByText("update ready")).toBeTruthy();
    expect(within(card("media")).getByText("needs approval")).toBeTruthy();
  });
});
