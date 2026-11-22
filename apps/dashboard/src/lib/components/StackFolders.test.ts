import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Stack } from "../api/client.ts";
import Harness from "./StackFoldersHarness.test.svelte";

const music = { path: "/var/mnt/pool/music", mount: "/music", read_only: true, updated_at: "2026-11-22T12:00:00Z" };
const stack = (folders: unknown[]): Stack => ({ name: "media", running: true, variables: [], folders }) as unknown as Stack;

afterEach(cleanup);

describe("StackFolders", () => {
  it("lists each folder with its mode, and one that the manifest overlaps as not bound", async () => {
    render(Harness, {
      props: { client: new QueryClient(), stack: stack([music, { ...music, mount: "/data/x", path: "/var/mnt/pool/x", shadowed_by: "its path DATA" }]) },
    });
    expect(screen.getByText("read-only")).toBeTruthy();
    expect(screen.getByText("not bound")).toBeTruthy();
    expect(screen.getByText(/overlaps its path DATA/)).toBeTruthy();
  });

  it("adds a folder read-only by default, keeping the others", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue({ data: stack([]), response: new Response(null, { status: 200 }) } as never);
    try {
      render(Harness, { props: { client: new QueryClient(), stack: stack([music]) } });
      await fireEvent.input(screen.getByLabelText("Host folder"), { target: { value: "/var/mnt/pool/films" } });
      await fireEvent.input(screen.getByLabelText("Mount in the stack"), { target: { value: "/films" } });
      await fireEvent.click(screen.getByRole("button", { name: "Add" }));
      expect(put).toHaveBeenCalledWith("/stacks/{name}/folders", {
        params: { path: { name: "media" } },
        body: {
          folders: [
            { path: music.path, mount: "/music", read_only: true },
            { path: "/var/mnt/pool/films", mount: "/films", read_only: true },
          ],
        },
      });
    } finally {
      put.mockRestore();
    }
  });
});
