import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client.ts";
import Harness from "./RestoreStacksDialogHarness.test.svelte";

afterEach(cleanup);

function pickFile(input: HTMLInputElement, name: string, content: string, type = "text/plain") {
  const file = new File([content], name, { type });
  Object.defineProperty(file, "text", { value: async () => content });
  return fireEvent.change(input, { target: { files: [file] } });
}

describe("RestoreStacksDialog", () => {
  it("restores from the host lock with an age key and displays restored, re-sealed, and skipped results", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue({
      data: { restored: ["immich"], resealed: ["immich", "caddy"], skipped: { plex: "already installed" } },
      response: new Response(null, { status: 200 }),
    } as never);
    const onClose = vi.fn();
    try {
      render(Harness, { props: { client: new QueryClient(), onClose } });
      await fireEvent.input(screen.getByLabelText("Or paste age key"), { target: { value: "  AGE-SECRET-KEY-1OLD  " } });
      await fireEvent.click(screen.getByRole("button", { name: "Restore" }));
      expect(await screen.findByText("Restored")).toBeTruthy();
      expect(post).toHaveBeenCalledWith("/stacks-lock/restore", {
        body: { use_host_lock: true, age_key: "AGE-SECRET-KEY-1OLD" },
      });
      expect(screen.getAllByText("immich")).toHaveLength(2);
      expect(screen.getByText("caddy")).toBeTruthy();
      expect(screen.getByText("plex: already installed")).toBeTruthy();
      await fireEvent.click(screen.getByRole("button", { name: "Done" }));
      expect(onClose).toHaveBeenCalled();
    } finally {
      post.mockRestore();
    }
  });

  it("restores from an uploaded stacks.lock.json file", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue({
      data: { restored: ["immich"], resealed: [], skipped: {} },
      response: new Response(null, { status: 200 }),
    } as never);
    try {
      render(Harness, { props: { client: new QueryClient(), onClose: () => {} } });
      const lockInput = screen.getByLabelText("Lock file (optional)") as HTMLInputElement;
      const entry = { name: "immich", source: "github:o/r", url: "https://example.com/r", rev: "aaaaaaa1", want_running: true, subnet: "10.99.0.0/29", api: false, gpu: false, ports: [], egress: [], storage: [], folders: [], manifest: "{}" };
      await pickFile(lockInput, "stacks.lock.json", JSON.stringify({ version: 1, stacks: [entry] }), "application/json");
      expect(screen.getByText("stacks.lock.json (1 stacks)")).toBeTruthy();
      await fireEvent.click(screen.getByRole("button", { name: "Restore" }));
      await vi.waitFor(() => expect(post).toHaveBeenCalled());
      expect(post).toHaveBeenCalledWith("/stacks-lock/restore", {
        body: { version: 1, stacks: [entry] },
      });
    } finally {
      post.mockRestore();
    }
  });

  it("rejects invalid lock JSON locally without calling the daemon", async () => {
    const post = vi.spyOn(api, "POST");
    try {
      render(Harness, { props: { client: new QueryClient(), onClose: () => {} } });
      const lockInput = screen.getByLabelText("Lock file (optional)") as HTMLInputElement;
      await pickFile(lockInput, "bad.json", "{not json", "application/json");
      expect(screen.getByRole("alert").textContent).toContain("Could not read lock file JSON.");
      expect((screen.getByRole("button", { name: "Restore" }) as HTMLButtonElement).disabled).toBe(true);
      expect(post).not.toHaveBeenCalled();
    } finally {
      post.mockRestore();
    }
  });
});
