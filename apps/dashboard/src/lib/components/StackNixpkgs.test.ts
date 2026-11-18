import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Stack } from "../api/client.ts";
import { keys } from "../api/queries.ts";
import Harness from "./StackNixpkgsHarness.test.svelte";

const running = "d".repeat(40);
const stack = (over: Partial<Stack> = {}, version = 7): Stack =>
  ({
    name: "ha",
    generation: 5,
    generations: [{ id: 5, rev: "r", built_at: "", failed: false, nixpkgs_rev: running }],
    nixpkgs_rev: running,
    approved: { rev: "r", url: "", at: "", manifest: { version, name: "ha", nixpkgs: "nixos-unstable" } },
    ...over,
  }) as unknown as Stack;

const withChannels = () => {
  const client = new QueryClient();
  client.setQueryData(keys.channels, ["nixos-unstable", "nixos-26.11", "nixos-26.05"]);
  return client;
};

afterEach(cleanup);

describe("StackNixpkgs", () => {
  it("offers the recommendation, the channels and a commit, and saves a choice", async () => {
    const patch = vi
      .spyOn(api, "PATCH")
      .mockResolvedValue({ data: stack({ nixpkgs: "nixos-26.11" }), response: new Response(null, { status: 200 }) } as never);
    try {
      render(Harness, { props: { client: withChannels(), stack: stack() } });
      const pick = screen.getByRole("combobox") as HTMLSelectElement;
      expect([...pick.options].map((o) => o.textContent)).toEqual([
        "Stack default (Unstable)",
        "Unstable",
        "26.11",
        "26.05",
        "Commit…",
      ]);
      expect((screen.getByRole("button", { name: "Apply" }) as HTMLButtonElement).disabled).toBe(true);
      await fireEvent.change(pick, { target: { value: "nixos-26.11" } });
      await fireEvent.click(screen.getByRole("button", { name: "Apply" }));
      expect(patch).toHaveBeenCalledWith("/stacks/{name}", { params: { path: { name: "ha" } }, body: { nixpkgs: "nixos-26.11" } });
    } finally {
      patch.mockRestore();
    }
  });

  it("asks for consent before going back to an older version", async () => {
    const patch = vi
      .spyOn(api, "PATCH")
      .mockResolvedValueOnce({
        error: { code: "nixpkgs_downgrade", detail: "older" },
        response: new Response(null, { status: 409 }),
      } as never)
      .mockResolvedValueOnce({ data: stack({ nixpkgs: "nixos-26.05" }), response: new Response(null, { status: 200 }) } as never);
    try {
      render(Harness, { props: { client: withChannels(), stack: stack() } });
      const save = () => screen.getByRole("button", { name: "Apply" }) as HTMLButtonElement;
      await fireEvent.change(screen.getByRole("combobox"), { target: { value: "nixos-26.05" } });
      await fireEvent.click(save());
      await vi.waitFor(() => expect(screen.getByText(/Older than the version running now/)).toBeTruthy());
      expect(save().disabled).toBe(true);
      await fireEvent.click(screen.getByLabelText(/Go back anyway/));
      expect(save().disabled).toBe(false);
      await fireEvent.click(save());
      expect(patch).toHaveBeenLastCalledWith("/stacks/{name}", {
        params: { path: { name: "ha" } },
        body: { nixpkgs: "nixos-26.05", allow_downgrade: true },
      });
    } finally {
      patch.mockRestore();
    }
  });

  it("takes a commit only when it is a full one", async () => {
    render(Harness, { props: { client: withChannels(), stack: stack() } });
    await fireEvent.change(screen.getByRole("combobox"), { target: { value: "commit" } });
    const input = screen.getByLabelText("nixpkgs commit");
    await fireEvent.input(input, { target: { value: "d5cc683" } });
    expect(screen.getByText(/full 40-character/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Apply" }) as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.input(input, { target: { value: "a".repeat(40) } });
    expect((screen.getByRole("button", { name: "Apply" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("shows the commit the next update builds with", () => {
    render(Harness, { props: { client: withChannels(), stack: stack({ nixpkgs: "nixos-26.11", nixpkgs_rev: "e".repeat(40) }) } });
    expect(screen.getByText("Next update")).toBeTruthy();
    expect((screen.getByRole("combobox") as HTMLSelectElement).value).toBe("nixos-26.11");
  });

  it("explains that an older stack chooses its own", () => {
    render(Harness, { props: { client: withChannels(), stack: stack({}, 6) } });
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(screen.getByText(/Set by the stack's repo/)).toBeTruthy();
  });
});
