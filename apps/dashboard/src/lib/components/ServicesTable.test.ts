import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type ServiceInfo, type Stack } from "../api/client.ts";
import Harness from "./ServicesTableHarness.test.svelte";

const unit = (name: string, active_state: string, stack = "media"): ServiceInfo =>
  ({ name, stack, active_state, sub_state: "", type: "Native", description: "", unit_name: name + ".service", started_at: null }) as ServiceInfo;

const manifest = { version: 9, name: "media", routes: [], storage: [], egress: [], gpu: false, health: { units: [] }, services: [{ unit: "plex.service", icon: "plex" }] };
const stack = (name = "media"): Stack =>
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

const show = (services: ServiceInfo[]) => render(Harness, { props: { client: new QueryClient(), stack: stack(), services } });

describe("ServicesTable", () => {
  afterEach(cleanup);

  it("names each service without its stack, with its icon and its disk use", () => {
    const { container } = show([{ ...unit("media-plex", "active"), disk_bytes: 2_400_000_000 }, unit("media-sonarr", "failed")]);

    expect(screen.getByText("plex")).toBeTruthy();
    expect(screen.getByText("failed")).toBeTruthy();
    // plex has an icon in the manifest; sonarr gets the letter tile.
    expect(container.querySelector("img")?.getAttribute("src")).toBe("/api/stacks/media/icon?unit=plex.service&rev=abcdef0123");
    expect(container.querySelectorAll("img")).toHaveLength(1);
    const cells = [...container.querySelectorAll("tbody tr")].map((r) => [...r.querySelectorAll("td")].map((c) => c.textContent?.trim()));
    expect(cells[0]![4]).toBe("2.4 GB");
    // sonarr has no measurement (it runs as root, or was never measured).
    expect(cells[1]![4]).toBe("–");
    expect(screen.queryByRole("link", { name: "Logs" })).toBeNull();
  });

  it("shows a disabled service as disabled, with Enable as its only action", async () => {
    const del = vi.spyOn(api, "DELETE").mockResolvedValue({ data: undefined, response: new Response(null, { status: 204 }) } as never);
    try {
      show([unit("media-plex", "active"), { ...unit("media-sonarr", "inactive"), masked: true }]);
      expect(screen.getByText("disabled")).toBeTruthy();
      await fireEvent.keyDown(screen.getByRole("button", { name: "More actions for sonarr" }), { key: "Enter" });
      const items = await screen.findAllByRole("menuitem");
      expect(items.map((i) => i.textContent?.trim())).toEqual(["Enable"]);
      await fireEvent.click(items[0]!);
      expect(del).toHaveBeenCalledWith("/services/{unit}/mask", { params: { path: { unit: "media-sonarr" } } });
    } finally {
      del.mockRestore();
    }
  });

  it("confirms before it disables a service", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue({ data: undefined, response: new Response(null, { status: 204 }) } as never);
    try {
      show([unit("media-plex", "active"), unit("media-sonarr", "active")]);
      await fireEvent.keyDown(screen.getByRole("button", { name: "More actions for plex" }), { key: "Enter" });
      await fireEvent.click(await screen.findByRole("menuitem", { name: "Disable…" }));
      expect(await screen.findByText("Disable plex?")).toBeTruthy();
      expect(screen.getByText("Stops now and stays stopped after restarts and updates.")).toBeTruthy();
      expect(put).not.toHaveBeenCalled();
      await fireEvent.click(screen.getByRole("button", { name: "Disable" }));
      expect(put).toHaveBeenCalledWith("/services/{unit}/mask", { params: { path: { unit: "media-plex" } } });
    } finally {
      put.mockRestore();
    }
  });

  it("does not offer to disable the last enabled service", async () => {
    show([unit("media-plex", "active"), { ...unit("media-sonarr", "inactive"), masked: true }]);
    await fireEvent.keyDown(screen.getByRole("button", { name: "More actions for plex" }), { key: "Enter" });
    expect((await screen.findByRole("menuitem", { name: "Disable…" })).hasAttribute("data-disabled")).toBe(true);
  });
});
