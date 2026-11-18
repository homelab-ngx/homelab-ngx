import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Stack } from "../api/client.ts";
import { keys } from "../api/queries.ts";
import Harness from "./RenameStackHarness.test.svelte";

const stack = (label = ""): Stack => ({ name: "media", label }) as unknown as Stack;
const answer = (s: Partial<Stack>) => ({ data: { ...stack(), ...s }, response: new Response(null, { status: 200 }) }) as never;

afterEach(cleanup);

describe("RenameStack", () => {
  it("saves the label and puts the stack the daemon returns in the cache", async () => {
    const patch = vi.spyOn(api, "PATCH").mockResolvedValue(answer({ label: "Movies & TV" }));
    const client = new QueryClient();
    const onClose = vi.fn();
    try {
      render(Harness, { props: { client, stack: stack(), onClose } });
      await fireEvent.input(screen.getByLabelText("Dashboard name"), { target: { value: "Movies & TV" } });
      await fireEvent.click(screen.getByRole("button", { name: "Save" }));
      await vi.waitFor(() => expect(onClose).toHaveBeenCalled());
      expect(patch).toHaveBeenCalledWith("/stacks/{name}", { params: { path: { name: "media" } }, body: { label: "Movies & TV" } });
      expect(client.getQueryData<Stack>([...keys.stacks, "media"])?.label).toBe("Movies & TV");
    } finally {
      patch.mockRestore();
    }
  });

  it("clears a label, and offers that only when there is one", async () => {
    const patch = vi.spyOn(api, "PATCH").mockResolvedValue(answer({ label: "" }));
    try {
      render(Harness, { props: { client: new QueryClient(), stack: stack("Movies & TV"), onClose: () => {} } });
      await fireEvent.click(screen.getByRole("button", { name: "Reset to media" }));
      expect(patch).toHaveBeenCalledWith("/stacks/{name}", { params: { path: { name: "media" } }, body: { label: "" } });
    } finally {
      patch.mockRestore();
    }
    cleanup();
    render(Harness, { props: { client: new QueryClient(), stack: stack(), onClose: () => {} } });
    expect(screen.queryByRole("button", { name: "Reset to media" })).toBeNull();
  });

  it("refuses a label that is too long before asking the daemon", async () => {
    const patch = vi.spyOn(api, "PATCH");
    try {
      render(Harness, { props: { client: new QueryClient(), stack: stack(), onClose: () => {} } });
      await fireEvent.input(screen.getByLabelText("Dashboard name"), { target: { value: "x".repeat(41) } });
      expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
      expect(screen.getByText(/At most 40 characters/)).toBeTruthy();
      expect(patch).not.toHaveBeenCalled();
    } finally {
      patch.mockRestore();
    }
  });
});
