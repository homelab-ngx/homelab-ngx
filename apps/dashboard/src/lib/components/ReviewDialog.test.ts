import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
import type { Stack } from "../api/client.ts";
import Harness from "./ReviewDialogHarness.test.svelte";

afterEach(cleanup);

const stack = (variables_ready: boolean): Stack =>
  ({
    name: "media",
    label: "Movies & TV",
    generation: 2,
    variables_ready,
    commits: [{ rev: "abcdef12", subject: "Bump version" }],
    new_grants: [{ kind: "storage", detail: "/srv at /data, read-only" }],
    fetched: { rev: "abcdef123456", manifest: { routes: [], storage: [], variables: [], egress: [], ports: [], gpu: false, containers: false, health: { units: [] } } },
    approved: { rev: "111111111111", manifest: { routes: [], storage: [], variables: [], egress: [], ports: [], gpu: false, containers: false, health: { units: [] } } },
    variables: variables_ready ? [] : [{ name: "API_KEY", kind: "secret", required: true, set: false, secret: true, in_env: false, declared: true, source: "unset", description: "" }],
  }) as unknown as Stack;

describe("ReviewDialog", () => {
  it("disables Approve and update and links to Configuration until required variables are ready", () => {
    render(Harness, { props: { client: new QueryClient(), stack: stack(false), onClose: () => {} } });

    const link = screen.getByRole("link", { name: "Open configuration" });
    expect(link.getAttribute("href")).toBe("/stacks/media/configuration");

    const btn = screen.getByRole("button", { name: "Approve and update" }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });

  it("enables Approve and update when variables are ready", () => {
    render(Harness, { props: { client: new QueryClient(), stack: stack(true), onClose: () => {} } });

    expect(screen.queryByRole("link", { name: "Open configuration" })).toBeNull();
    const btn = screen.getByRole("button", { name: "Approve and update" }) as HTMLButtonElement;
    expect(btn.disabled).toBe(false);
  });
});
