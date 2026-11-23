import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "#lib/api/client.ts";
import Harness from "./DeleteEntryDialogHarness.test.svelte";

const answer = (data: unknown, status = 200) => ({ data, response: new Response(null, { status }) }) as never;

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const show = (path: string, type: "dir" | "file" | "symlink", onDeleted = vi.fn()) =>
  render(Harness, { props: { client: new QueryClient(), path, type, onDeleted } });

describe("DeleteEntryDialog", () => {
  it("deletes a file without typing its name", async () => {
    const del = vi.spyOn(api, "DELETE").mockResolvedValue(answer(null, 204));
    const onDeleted = vi.fn();
    show("/srv/app/app.conf", "file", onDeleted);
    await fireEvent.click(await screen.findByRole("button", { name: "Delete file" }));
    await vi.waitFor(() => expect(onDeleted).toHaveBeenCalled());
    expect(del).toHaveBeenCalledWith("/storage/entry", { params: { query: { path: "/srv/app/app.conf", confirm: undefined } } });
  });

  it("needs a folder's name typed when it holds anything", async () => {
    vi.spyOn(api, "GET").mockResolvedValue(answer({ path: "/srv/app/data", root: "/srv", entries: [], total: 3, truncated: false }));
    const del = vi.spyOn(api, "DELETE").mockResolvedValue(answer(null, 204));
    show("/srv/app/data", "dir");
    const button = await screen.findByRole("button", { name: "Delete folder" });
    expect(await screen.findByText(/with its 3 items/)).toBeTruthy();
    expect((button as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.input(screen.getByLabelText("Type data"), { target: { value: "data" } });
    expect((button as HTMLButtonElement).disabled).toBe(false);
    await fireEvent.click(button);
    await vi.waitFor(() => expect(del).toHaveBeenCalledWith("/storage/entry", { params: { query: { path: "/srv/app/data", confirm: "data" } } }));
  });

  it("deletes an empty folder without its name", async () => {
    vi.spyOn(api, "GET").mockResolvedValue(answer({ path: "/srv/app/data", root: "/srv", entries: [], total: 0, truncated: false }));
    show("/srv/app/data", "dir");
    expect(await screen.findByText("The folder is empty.")).toBeTruthy();
    expect(screen.queryByLabelText("Type data")).toBeNull();
    expect((screen.getByRole("button", { name: "Delete folder" }) as HTMLButtonElement).disabled).toBe(false);
  });
});
