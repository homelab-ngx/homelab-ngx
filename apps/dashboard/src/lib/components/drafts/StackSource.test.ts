import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { beforeNavigate, goto } from "$app/navigation";
import { api, type Schema, type Stack } from "#lib/api/client.ts";
import Harness from "./StackSourceHarness.test.svelte";

vi.mock("$app/navigation", () => ({ beforeNavigate: vi.fn(), goto: vi.fn(async () => {}) }));
vi.mock("#lib/components/CodeEditor.svelte", async () => ({ default: (await import("./FakeEditor.test.svelte")).default }));

const manifest = '{ "version": 9 }\n';
const files = {
  source: "github:homelab-ngx/stack-media",
  rev: "abcdef0123",
  files: [
    { path: "flake.nix", size: 2, binary: false, content: "{}" },
    { path: "homelab-stack.json", size: manifest.length, binary: false, content: manifest },
    { path: "icon.png", size: 2048, binary: true, content: "" },
  ],
} as Schema<"StackFiles">;
const stack = (source = files.source, approved = true) =>
  ({ name: "media", label: "Media", source, approved: approved ? { rev: "abcdef0123", url: "", at: "", manifest: {} } : null }) as unknown as Stack;
const draft = { name: "media", files: files.files, installed_as: ["media"] };
const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;

const show = (s = stack(), onUnsaved = vi.fn()) => render(Harness, { props: { client: new QueryClient(), stack: s, files, onUnsaved } });
const type = (text: string) => fireEvent.input(screen.getByLabelText("homelab-stack.json"), { target: { value: text } });

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.mocked(goto).mockClear();
  vi.mocked(beforeNavigate).mockClear();
});

describe("StackSource", () => {
  it("lists the files with nothing to make, rename or delete", () => {
    show();
    expect(screen.getByRole("button", { name: "flake.nix" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "New file" })).toBeNull();
    expect(screen.queryByRole("button", { name: /More actions for flake.nix/ })).toBeNull();
    expect(screen.getByText(/Read only/)).toBeTruthy();
  });

  it("says a change cannot be saved once something is typed, and Discard takes it back", async () => {
    show();
    expect(screen.queryByRole("status")).toBeNull();
    await type('{ "version": 10 }\n');
    expect(screen.getByRole("status").textContent).toMatch(/github.com\/homelab-ngx\/stack-media is read only here/);
    await fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("forks the stack, then saves what was typed into the copy", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue(ok(draft));
    const put = vi.spyOn(api, "PUT").mockResolvedValue(ok(draft));
    show();
    await type('{ "version": 10 }\n');
    await fireEvent.click(screen.getByRole("button", { name: "Fork and save" }));
    await vi.waitFor(() => expect(put).toHaveBeenCalled());
    expect(post).toHaveBeenCalledWith("/stacks/{name}/fork", { params: { path: { name: "media" } } });
    expect(put).toHaveBeenCalledWith("/drafts/{name}", {
      params: { path: { name: "media" } },
      body: { changes: [{ path: "homelab-stack.json", content: '{ "version": 10 }\n' }] },
    });
  });

  it("hands on the typed files when the fork is made but the save fails", async () => {
    vi.spyOn(api, "POST").mockResolvedValue(ok(draft));
    vi.spyOn(api, "PUT").mockRejectedValue(new Error("disk full"));
    const onUnsaved = vi.fn();
    show(stack(), onUnsaved);
    await type("changed");
    await fireEvent.click(screen.getByRole("button", { name: "Fork and save" }));
    await vi.waitFor(() => expect(onUnsaved).toHaveBeenCalled());
    const [buffer, error] = onUnsaved.mock.calls[0]!;
    expect(buffer["homelab-stack.json"]).toBe("changed");
    expect(error).toMatch(/disk full/);
  });

  it("tells a folder's stack to edit the folder and commit", async () => {
    show(stack("git+file:///srv/stacks/media"));
    await type("changed");
    expect(screen.getByRole("status").textContent).toMatch(/Edit the files in their folder and commit/);
  });

  it("confirms in a modal before leaving with unsaved changes", async () => {
    show();
    await type("changed");
    const guard = vi.mocked(beforeNavigate).mock.calls.at(-1)![0];
    const cancel = vi.fn();
    guard({ type: "link", to: { url: new URL("http://host/stacks/media") }, cancel } as never);
    expect(cancel).toHaveBeenCalled();
    expect(await screen.findByRole("heading", { name: "Leave without saving?" })).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Leave" }));
    expect(goto).toHaveBeenCalledWith("/stacks/media");
  });

  it("explains why Fork is disabled before a stack is approved", () => {
    show(stack(files.source, false));
    const btn = screen.getByRole("button", { name: "Fork…" }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(btn.title).toBe("Approve the stack first.");
  });
});
