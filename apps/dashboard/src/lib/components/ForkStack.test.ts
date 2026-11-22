import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Stack } from "../api/client.ts";
import Harness from "./ForkStackHarness.test.svelte";

const goto = vi.hoisted(() => vi.fn());
vi.mock("$app/navigation", () => ({ goto }));

const stack = { name: "photos", label: "", source: "github:o/photos" } as unknown as Stack;
const ok = (name: string) => ({ data: { name }, response: new Response(null, { status: 201 }) }) as never;

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  goto.mockReset();
});

describe("ForkStack", () => {
  it("moves the stack onto a draft of its name and opens it", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue(ok("photos"));
    const onClose = vi.fn();
    render(Harness, { props: { client: new QueryClient(), stack, onClose } });
    await fireEvent.click(screen.getByRole("button", { name: "Fork" }));
    expect(post).toHaveBeenCalledWith("/stacks/{name}/fork", { params: { path: { name: "photos" } } });
    await vi.waitFor(() => expect(goto).toHaveBeenCalledWith("/stacks/drafts/photos"));
    expect(onClose).toHaveBeenCalled();
  });

  it("copies to a new draft under a name of its own", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue(ok("mine"));
    render(Harness, { props: { client: new QueryClient(), stack, onClose: vi.fn() } });
    await fireEvent.click(screen.getByLabelText(/Copy to a new draft/));
    const fork = screen.getByRole("button", { name: "Fork" }) as HTMLButtonElement;
    expect(fork.disabled).toBe(true);
    await fireEvent.input(screen.getByLabelText("Draft name"), { target: { value: "Mine" } });
    expect(screen.getByText(/1-12 characters/)).toBeTruthy();
    await fireEvent.input(screen.getByLabelText("Draft name"), { target: { value: "mine" } });
    await fireEvent.click(fork);
    expect(post).toHaveBeenCalledWith("/drafts", { body: { name: "mine", from: "photos" } });
    await vi.waitFor(() => expect(goto).toHaveBeenCalledWith("/stacks/drafts/mine"));
  });
});
