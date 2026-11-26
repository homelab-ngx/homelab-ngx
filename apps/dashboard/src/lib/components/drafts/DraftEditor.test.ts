import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { beforeNavigate, goto } from "$app/navigation";
import { api, type Draft } from "#lib/api/client.ts";
import Harness from "./DraftEditorHarness.test.svelte";

vi.mock("$app/navigation", () => ({ beforeNavigate: vi.fn(), goto: vi.fn(async () => {}) }));
vi.mock("#lib/components/CodeEditor.svelte", async () => ({ default: (await import("./FakeEditor.test.svelte")).default }));

const manifest = '{ "version": 10 }\n';
const baseDraft = (overrides: Partial<Draft> = {}): Draft =>
  ({
    name: "demo",
    source: "git+file:///var/lib/homelab-daemon/drafts/demo",
    rev: "abcdef0123",
    created_at: "2026-11-26T10:00:00Z",
    saved_at: "2026-11-26T12:00:00Z",
    installed_as: [],
    files: [
      { path: "homelab-stack.json", size: manifest.length, binary: false, content: manifest },
      { path: "flake.nix", size: 2, binary: false, content: "{}" },
    ],
    ...overrides,
  }) as Draft;

const ok = (data: unknown, status = 200) => ({ data, response: new Response(null, { status }) }) as never;

beforeEach(() => {
  localStorage.removeItem("draft-assistant-open");
  vi.spyOn(api, "GET").mockImplementation(((route: string) => {
    if (route === "/settings/assistant") return Promise.resolve(ok({ provider: "none", model: "", audit_model: "", base_url: "", key_set: false }));
    if (route === "/stacks-manifest-schema") return Promise.resolve(ok({}));
    return Promise.resolve(ok({}));
  }) as never);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.mocked(goto).mockClear();
  vi.mocked(beforeNavigate).mockClear();
});

const show = (draft = baseDraft()) => render(Harness, { props: { client: new QueryClient(), draft } });
const typeManifest = (text: string) => fireEvent.input(screen.getByLabelText("homelab-stack.json"), { target: { value: text } });

describe("DraftEditor", () => {
  it("keeps Install primary while clean and moves primary to Save when dirty", async () => {
    show();
    const saveBtn = screen.getByRole("button", { name: "Saved" });
    const installBtn = screen.getByRole("button", { name: "Install…" });
    expect(installBtn.className).toContain("btn-primary");
    expect(saveBtn.className).not.toContain("btn-primary");

    await typeManifest('{ "version": 10, "name": "demo" }\n');
    const dirtySave = screen.getByRole("button", { name: "Save" });
    expect(dirtySave.className).toContain("btn-primary");
    expect(screen.getByRole("button", { name: "Install…" }).className).not.toContain("btn-primary");
  });

  it("discards unsaved changes through a confirmation modal", async () => {
    show();
    await typeManifest('{ "changed": true }\n');
    await fireEvent.keyDown(screen.getByRole("button", { name: "More actions for demo" }), { key: "Enter" });
    await fireEvent.click(await screen.findByRole("menuitem", { name: "Discard changes…" }));
    expect(await screen.findByRole("heading", { name: "Discard unsaved changes?" })).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect((screen.getByLabelText("homelab-stack.json") as HTMLTextAreaElement).value).toBe(manifest);
    expect(screen.getByRole("button", { name: "Saved" })).toBeTruthy();
  });

  it("confirms draft deletion in a modal before deleting", async () => {
    const del = vi.spyOn(api, "DELETE").mockResolvedValue(ok(null, 204));
    show();
    await fireEvent.keyDown(screen.getByRole("button", { name: "More actions for demo" }), { key: "Enter" });
    await fireEvent.click(await screen.findByRole("menuitem", { name: "Delete draft…" }));
    expect(await screen.findByRole("heading", { name: "Delete demo?" })).toBeTruthy();
    expect(del).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await vi.waitFor(() => expect(del).toHaveBeenCalledWith("/drafts/{name}", { params: { path: { name: "demo" } } }));
    await vi.waitFor(() => expect(goto).toHaveBeenCalledWith("/stacks"));
  });

  it("confirms in a modal before leaving with unsaved edits", async () => {
    show();
    await typeManifest("edited\n");
    const guard = vi.mocked(beforeNavigate).mock.calls.at(-1)![0];
    const cancel = vi.fn();
    guard({ type: "link", to: { url: new URL("http://host/stacks?tab=all") }, cancel } as never);
    expect(cancel).toHaveBeenCalled();
    expect(await screen.findByRole("heading", { name: "Leave without saving?" })).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Leave" }));
    expect(goto).toHaveBeenCalledWith("/stacks?tab=all");
  });
});
