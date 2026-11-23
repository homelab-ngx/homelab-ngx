import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { goto } from "$app/navigation";
import { api, type Schema } from "#lib/api/client.ts";
import Harness from "./HostEditorHarness.test.svelte";

vi.mock("$app/navigation", () => ({ beforeNavigate: vi.fn(), goto: vi.fn() }));
vi.mock("#lib/components/CodeEditor.svelte", async () => ({ default: (await import("../drafts/FakeEditor.test.svelte")).default }));

type Entry = Schema<"Entry">;
const entry = (name: string, type: Entry["type"]): Entry => ({ name, type, size: 1, disk_bytes: 1, modified_at: "2026-11-17T10:00:00Z", mode: "", owner: "root", group: "root" });
const answer = (data: unknown, status = 200) => ({ data, response: new Response(null, { status }) }) as never;
const problem = (status: number, code: string, detail: string) => ({ error: { code, detail }, response: new Response(null, { status }) }) as never;

const folders: Record<string, Entry[]> = {
  "/srv/app": [entry("app.conf", "file"), entry("data", "dir")],
  "/srv/app/data": [entry("db.json", "file")],
};
const file = { path: "/srv/app/app.conf", root: "/srv", size: 6, modified_at: "2026-11-17T10:00:00Z", version: "v1", binary: false, content: "a = 1\n" };

let get: ReturnType<typeof vi.spyOn>;
beforeEach(() => {
  get = vi.spyOn(api, "GET").mockImplementation(((route: string, opts: { params: { query: { path: string } } }) => {
    const path = opts.params.query.path;
    if (route === "/storage/browse") return Promise.resolve(answer({ path, root: "/srv", entries: folders[path] ?? [], total: 0, truncated: false }));
    return Promise.resolve(answer(file));
  }) as never);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.mocked(goto).mockClear();
});

const show = () => render(Harness, { props: { client: new QueryClient(), root: "/srv/app", path: "/srv/app/app.conf" } });
const type = async (text: string) => fireEvent.input(await screen.findByLabelText("app.conf"), { target: { value: text } });

describe("HostEditor", () => {
  it("opens the file beside its folder, whose folders are read when opened", async () => {
    show();
    expect(((await screen.findByLabelText("app.conf")) as HTMLTextAreaElement).value).toBe("a = 1\n");
    const data = await screen.findByRole("button", { name: "data/" });
    expect(data.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("button", { name: "db.json" })).toBeNull();
    await fireEvent.click(data);
    expect(await screen.findByRole("button", { name: "db.json" })).toBeTruthy();
    expect(get).toHaveBeenCalledWith("/storage/browse", { params: { query: { path: "/srv/app/data" } } });
    await fireEvent.click(screen.getByRole("button", { name: "db.json" }));
    expect(goto).toHaveBeenCalledWith("/storage/edit?path=%2Fsrv%2Fapp%2Fdata%2Fdb.json&root=%2Fsrv%2Fapp", expect.anything());
  });

  it("saves with the version it read, and typing the old text back is no change", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue(answer({ ...file, version: "v2", content: undefined }));
    show();
    await type("a = 2\n");
    expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();
    await type("a = 1\n");
    expect(screen.getByRole("button", { name: "Saved" })).toBeTruthy();
    await type("a = 2\n");
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(screen.getByRole("button", { name: "Saved" })).toBeTruthy());
    expect(put).toHaveBeenCalledWith("/storage/file", { body: { path: "/srv/app/app.conf", content: "a = 2\n", version: "v1" } });
  });

  it("asks before saving over a change made on disk", async () => {
    const put = vi
      .spyOn(api, "PUT")
      .mockResolvedValueOnce(problem(409, "changed_on_disk", "the file changed on disk since it was read"))
      .mockResolvedValue(answer({ ...file, version: "v3", content: undefined }));
    show();
    await type("a = 2\n");
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect((await screen.findByRole("alert")).textContent).toMatch(/changed on disk/);
    await fireEvent.click(screen.getByRole("button", { name: "Save anyway" }));
    await vi.waitFor(() => expect(put).toHaveBeenCalledTimes(2));
    expect(put).toHaveBeenLastCalledWith("/storage/file", { body: { path: "/srv/app/app.conf", content: "a = 2\n", version: undefined } });
  });

  it("saves as a new file and opens it", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue(answer({ ...file, path: "/srv/app/copy.conf", content: undefined }));
    show();
    await screen.findByLabelText("app.conf");
    await fireEvent.click(screen.getByRole("button", { name: "Save as…" }));
    await fireEvent.input(screen.getByLabelText("Save as"), { target: { value: "copy.conf" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(goto).toHaveBeenCalled());
    expect(put).toHaveBeenCalledWith("/storage/file", { body: { path: "/srv/app/copy.conf", content: "a = 1\n", create: true } });
    expect(goto).toHaveBeenCalledWith("/storage/edit?path=%2Fsrv%2Fapp%2Fcopy.conf", expect.anything());
  });

  it("makes a new file on disk at once", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue(answer({ ...file, path: "/srv/app/data/new.conf", content: undefined }));
    show();
    await screen.findByRole("button", { name: "data/" });
    await fireEvent.click(screen.getByRole("button", { name: "New file" }));
    const field = screen.getByLabelText("New file's path");
    await fireEvent.input(field, { target: { value: "app.conf" } });
    expect(screen.getByRole("alert").textContent).toBe("app.conf is already there.");
    await fireEvent.input(field, { target: { value: "data/new.conf" } });
    await fireEvent.submit(field.closest("form")!);
    await vi.waitFor(() => expect(goto).toHaveBeenCalled());
    expect(put).toHaveBeenCalledWith("/storage/file", { body: { path: "/srv/app/data/new.conf", content: "", create: true } });
  });
});
