import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type DraftAnswer } from "#lib/api/client.ts";
import type { Buffer } from "#lib/drafts.ts";
import AssistantPanel from "./AssistantPanel.svelte";

vi.mock("./DiffView.svelte", async () => ({ default: (await import("./FakeDiffView.test.svelte")).default }));

function sseStream(events: { event: string; data: unknown }[]) {
  const payload = events.map((e) => `event: ${e.event}\ndata: ${JSON.stringify(e.data)}\n\n`).join("");
  return new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(payload));
      controller.close();
    },
  });
}

const baseBuffer = (): Buffer => ({
  "homelab-stack.json": '{ "version": 10 }\n',
  "flake.nix": "{}\n",
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("AssistantPanel", () => {
  it("renders compact mode choices and disables Fix error when nothing failed", () => {
    render(AssistantPanel, {
      props: { name: "demo", buffer: baseBuffer(), unsaved: [], open: "homelab-stack.json", fixText: "", model: "claude-opus-5-5", onApply: vi.fn() },
    });
    expect(screen.getByRole("radio", { name: "Draft" })).toBeTruthy();
    expect(screen.getByRole("radio", { name: "Ask" })).toBeTruthy();
    expect(screen.getByRole("radio", { name: "Review" })).toBeTruthy();
    const fix = screen.getByRole("radio", { name: "Fix error" }) as HTMLInputElement;
    expect(fix.disabled).toBe(true);
    expect(fix.closest("label")?.title).toMatch(/No/);
  });

  it("submits with Ctrl+Enter, ranks Apply all primary for multi-file diffs, and allows Clear", async () => {
    const answer: DraftAnswer = {
      model: "claude-opus-5-5",
      usage: { input_tokens: 100, output_tokens: 50, cost: 0.01 },
      summary: "Updated both files.",
      changes: ["Updated manifest", "Updated flake"],
      todo: [],
      notes: "",
      dropped: [],
      files: [
        { path: "homelab-stack.json", content: '{ "version": 10, "name": "demo" }\n' },
        { path: "flake.nix", content: "{ description = \"demo\"; }\n" },
      ],
    };
    const post = vi.spyOn(api, "POST").mockResolvedValue({
      data: sseStream([{ event: "answer", data: answer }]),
      response: new Response(null, { status: 200 }),
    } as never);

    let buffer = baseBuffer();
    const onApply = vi.fn((next: Buffer) => {
      buffer = next;
    });
    const view = render(AssistantPanel, {
      props: { name: "demo", buffer, unsaved: [], open: "homelab-stack.json", fixText: "", model: "claude-opus-5-5", onApply },
    });

    const box = screen.getByPlaceholderText("Describe the stack");
    await fireEvent.input(box, { target: { value: "Add a demo service" } });
    await fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(1));

    const applyAll = await screen.findByRole("button", { name: "Apply all" });
    expect(applyAll.className).toContain("btn-primary");
    expect(screen.getByRole("button", { name: "Discard all" })).toBeTruthy();

    const applyButtons = screen.getAllByRole("button", { name: "Apply" });
    expect(applyButtons).toHaveLength(2);
    expect(applyButtons[0]!.className).not.toContain("btn-primary");

    // Applying one leaves one file whose single Apply button becomes primary.
    await fireEvent.click(applyButtons[0]!);
    expect(onApply).toHaveBeenCalledTimes(1);
    await view.rerender({ buffer });
    const remainingApply = screen.getByRole("button", { name: "Apply" });
    expect(remainingApply.className).toContain("btn-primary");

    await fireEvent.click(remainingApply);
    expect(await screen.findByText("Applied.")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(screen.queryByText("Updated both files.")).toBeNull();
  });
});
