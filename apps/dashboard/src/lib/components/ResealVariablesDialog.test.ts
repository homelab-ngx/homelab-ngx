import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Stack } from "../api/client.ts";
import { keys } from "../api/queries.ts";
import Harness from "./ResealVariablesDialogHarness.test.svelte";

const stack = (): Stack =>
  ({
    name: "media",
    label: "Movies & TV",
    variables_ready: false,
    variables_key_mismatch: true,
    variables_error: "variables are sealed to another host's age key",
    variables: [],
  }) as unknown as Stack;

afterEach(cleanup);

describe("ResealVariablesDialog", () => {
  it("disables Unlock until an age key is provided", () => {
    render(Harness, { props: { client: new QueryClient(), stack: stack(), onClose: () => {} } });
    expect((screen.getByRole("button", { name: "Unlock" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("posts the age key, updates the stack cache, and closes on success", async () => {
    const updated = { ...stack(), variables_ready: true, variables_key_mismatch: false, variables_error: "" } as Stack;
    const post = vi.spyOn(api, "POST").mockResolvedValue({ data: updated, response: new Response(null, { status: 200 }) } as never);
    const client = new QueryClient();
    const onClose = vi.fn();
    try {
      render(Harness, { props: { client, stack: stack(), onClose } });
      await fireEvent.input(screen.getByLabelText("Or paste age key"), { target: { value: "  AGE-SECRET-KEY-1TESTKEY  " } });
      await fireEvent.click(screen.getByRole("button", { name: "Unlock" }));
      await vi.waitFor(() => expect(onClose).toHaveBeenCalled());
      expect(post).toHaveBeenCalledWith("/stacks/{name}/variables/reseal", {
        params: { path: { name: "media" } },
        body: { age_key: "AGE-SECRET-KEY-1TESTKEY" },
      });
      expect(client.getQueryData<Stack>([...keys.stacks, "media"])?.variables_ready).toBe(true);
    } finally {
      post.mockRestore();
    }
  });

  it("shows daemon errors on wrong age key", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue({
      error: { status: 422, code: "wrong_age_key", detail: 'the given age key does not unlock stack "media"\'s variables' },
      response: new Response(null, { status: 422 }),
    } as never);
    const onClose = vi.fn();
    try {
      render(Harness, { props: { client: new QueryClient(), stack: stack(), onClose } });
      await fireEvent.input(screen.getByLabelText("Or paste age key"), { target: { value: "AGE-SECRET-KEY-1WRONG" } });
      await fireEvent.click(screen.getByRole("button", { name: "Unlock" }));
      expect(await screen.findByRole("alert")).toBeTruthy();
      expect(screen.getByText(/does not unlock stack "media"/)).toBeTruthy();
      expect(onClose).not.toHaveBeenCalled();
    } finally {
      post.mockRestore();
    }
  });
});
