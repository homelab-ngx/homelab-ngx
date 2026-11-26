import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type StackSSHKeys } from "#lib/api/client.ts";
import Harness from "./StackKeysCardHarness.test.svelte";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const payload: StackSSHKeys = {
  host: {
    public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHostKey homelab-ngx",
    fingerprint: "SHA256:hostfp123",
  },
  stacks: [
    {
      stack: "photos",
      repo: "github.com/alice/photos-stack",
      public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPhotosKey homelab-ngx:alice/photos-stack",
      fingerprint: "SHA256:photosfp456",
    },
  ],
};

describe("StackKeysCard", () => {
  it("shows the host key, its fingerprint, the account-level note and per-stack keys", async () => {
    vi.spyOn(api, "GET").mockResolvedValue({ data: payload, response: new Response(null, { status: 200 }) } as never);
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });

    render(Harness, { props: { client: new QueryClient() } });

    await vi.waitFor(() => expect(screen.getByText(payload.host.public_key)).toBeTruthy());
    expect(screen.getByText("SHA256:hostfp123")).toBeTruthy();
    expect(screen.getByText(/user or machine account/)).toBeTruthy();
    expect(screen.getByText(/every repository that account reaches/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "photos" }).getAttribute("href")).toBe("/stacks/photos");
    expect(screen.getByText("SHA256:photosfp456")).toBeTruthy();

    const copyButtons = screen.getAllByRole("button", { name: "Copy" });
    await fireEvent.click(copyButtons[0]!);
    expect(writeText).toHaveBeenCalledWith(payload.host.public_key);
    await fireEvent.click(copyButtons[1]!);
    expect(writeText).toHaveBeenCalledWith(payload.stacks[0]!.public_key);
  });
});
