import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client.ts";
import Harness from "./PathInputHarness.test.svelte";

const tree: Record<string, string[]> = {
  "/": ["var"],
  "/var/": ["mnt"],
  "/var/mnt/": ["pool", "usb"],
  "/var/mnt/pool/": ["media", "music"],
};

function listings() {
  return vi.spyOn(api, "GET").mockImplementation(((_: string, init: { params: { query: { path: string } } }) => {
    const path = init.params.query.path;
    const names = tree[path];
    if (!names) return Promise.resolve({ error: { status: 403, title: "Forbidden", detail: "outside" }, response: new Response(null, { status: 403 }) });
    const entries = names.map((name) => ({ name, type: "dir" }));
    return Promise.resolve({ data: { path, root: "", entries, total: entries.length, truncated: false }, response: new Response(null, { status: 200 }) });
  }) as never);
}

afterEach(cleanup);

describe("PathInput", () => {
  it("suggests the folders under what is typed and descends into a pick", async () => {
    const get = listings();
    const onpick = vi.fn();
    try {
      render(Harness, { props: { client: new QueryClient(), onpick } });
      const field = screen.getByRole("combobox", { name: "Host folder" });
      await fireEvent.focus(field);
      await fireEvent.input(field, { target: { value: "/var/mnt/" } });
      await waitFor(() => expect(screen.getAllByRole("option").map((o) => o.textContent?.trim())).toEqual(["/var/mnt/pool", "/var/mnt/usb"]));
      await fireEvent.input(field, { target: { value: "/var/mnt/p" } });
      await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
      await fireEvent.keyDown(field, { key: "Tab" });
      expect((field as HTMLInputElement).value).toBe("/var/mnt/pool/");
      expect(onpick).toHaveBeenCalledWith("/var/mnt/pool");
      await waitFor(() => expect(screen.getAllByRole("option").map((o) => o.textContent?.trim())).toEqual(["/var/mnt/pool/media", "/var/mnt/pool/music"]));
      await fireEvent.keyDown(field, { key: "ArrowDown" });
      await fireEvent.keyDown(field, { key: "ArrowDown" });
      await fireEvent.keyDown(field, { key: "Enter" });
      expect((field as HTMLInputElement).value).toBe("/var/mnt/pool/music/");
    } finally {
      get.mockRestore();
    }
  });

  it("drops the trailing slash on blur, and shows nothing outside the browsable folders", async () => {
    const get = listings();
    try {
      render(Harness, { props: { client: new QueryClient() } });
      const field = screen.getByRole("combobox", { name: "Host folder" });
      await fireEvent.focus(field);
      await fireEvent.input(field, { target: { value: "/srv/" } });
      await waitFor(() => expect(get).toHaveBeenCalled());
      expect(screen.queryAllByRole("option")).toHaveLength(0);
      expect(field.getAttribute("aria-expanded")).toBe("false");
      await fireEvent.blur(field);
      expect(screen.getByRole("status").textContent).toBe("/srv");
    } finally {
      get.mockRestore();
    }
  });
});
