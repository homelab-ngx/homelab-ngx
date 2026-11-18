import { QueryClient } from "@tanstack/svelte-query";
import { render, screen } from "@testing-library/svelte";
import { describe, expect, it } from "vitest";
import type { ServiceInfo, Stack } from "../api/client.ts";
import Harness from "./StackTableHarness.test.svelte";

const unit = (name: string, active_state: string, stack = "media"): ServiceInfo =>
  ({ name, stack, active_state, sub_state: "", type: "Native", description: "", unit_name: name, started_at: null }) as ServiceInfo;

const manifest = { version: 1, name: "x", routes: [], storage: [], egress: [], gpu: false, health: { units: [] } };
const stack = (name: string): Stack =>
  ({
    name,
    source: "github:o/" + name,
    running: true,
    want_running: true,
    generation: 2,
    generations: [{ id: 2, rev: "abcdef0123", built_at: "", failed: false }],
    fetched: { rev: "abcdef0123", url: "", at: "", manifest },
    approved: { rev: "abcdef0123", url: "", at: "", manifest },
    update_state: "up_to_date",
    commits: [],
    new_grants: [],
    variables_ready: true,
    restart_needed: false,
    variables: [],
  }) as unknown as Stack;

describe("StackTable", () => {
  it("shows a failed service without a click and keeps healthy stacks closed (AE1)", () => {
    const services = [unit("media-sonarr", "failed"), unit("media-plex", "active"), unit("email-bridge", "active", "email")];
    render(Harness, { props: { client: new QueryClient(), stacks: [stack("email"), stack("media")], services } });

    expect(screen.getByText(/Degraded\s1\/2/)).toBeTruthy();
    expect(screen.getByText("sonarr")).toBeTruthy();
    expect(screen.getByText("plex")).toBeTruthy();
    // email is healthy and closed: its services are behind the toggle.
    expect(screen.queryByText("bridge")).toBeNull();
    expect(screen.getByRole("button", { name: /email/ }).getAttribute("aria-expanded")).toBe("false");
  });

  it("names a stack by its label and still finds its services by name", () => {
    const labelled = { ...stack("media"), label: "Movies & TV" } as Stack;
    render(Harness, { props: { client: new QueryClient(), stacks: [labelled], services: [unit("media-sonarr", "failed")] } });

    expect(screen.getByRole("button", { name: /Movies & TV/ })).toBeTruthy();
    expect(screen.getAllByText("sonarr").length).toBeGreaterThan(0);
    expect(screen.getByRole("link", { name: "Open Movies & TV" }).getAttribute("href")).toBe("/stacks/media");
  });

  it("offers Update for new commits and Review when the update asks for new access", () => {
    const fresh = { ...stack("email"), update_state: "commits_available", commits: [{ rev: "1234567", subject: "Bump" }] } as unknown as Stack;
    const asks = { ...stack("media"), update_state: "needs_approval", commits: [] } as unknown as Stack;
    render(Harness, { props: { client: new QueryClient(), stacks: [fresh, asks], services: [] } });

    expect(screen.getByText("1 commit")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Update" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Review…" })).toBeTruthy();
  });
});
