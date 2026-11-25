import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client.ts";
import type { Preview, PreviewItem } from "../../backups.ts";
import Harness from "./PreviewViewHarness.test.svelte";

const item = (over: Partial<PreviewItem>): PreviewItem => ({
  kind: "folder", destination: "Bucket", label: "Library", path: "/pool/photos", result: "success", error: "", note: "", errors: 0,
  new: 0, new_bytes: 0, changed: 0, changed_bytes: 0, unchanged: 0, extra: 0, deletes: false,
  new_sample: [], changed_sample: [], extra_sample: [], ...over,
});
const done: Preview = {
  backup_id: 1, started_at: "2026-11-16T04:00:00Z", finished_at: "2026-11-16T04:01:30Z", result: "success", error: "", progress: null,
  items: [
    item({ kind: "destination", label: "", path: "" }),
    item({ new: 7, new_bytes: 4_000_000, changed: 2, changed_bytes: 1000, unchanged: 40, extra: 3, deletes: true, new_sample: ["2026/a.jpg"], extra_sample: ["old.zip"] }),
    item({ kind: "destination", destination: "Repo", label: "", path: "", result: "skipped", note: "restic destinations are not previewed" }),
    item({ destination: "Repo", result: "skipped" }),
  ],
};

const answer = (data: unknown) => ({ data, response: new Response() }) as never;
const mount = (props: { has: boolean; previewing: boolean }) => render(Harness, { props: { client: new QueryClient(), ...props } });

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("PreviewView", () => {
  it("asks for nothing and shows nothing when the backup has no preview", () => {
    const get = vi.spyOn(api, "GET");
    mount({ has: false, previewing: false });
    expect(get).not.toHaveBeenCalled();
    expect(screen.queryByText("Preview")).toBeNull();
  });

  it("shows what a run would do per folder, and the paths on demand", async () => {
    vi.spyOn(api, "GET").mockResolvedValue(answer(done));
    mount({ has: true, previewing: false });
    await waitFor(() => expect(screen.getByText("Nothing copied", { exact: false })).toBeTruthy());
    // The destination deletes, so the last column says so.
    expect(screen.getByRole("columnheader", { name: "Deleted" })).toBeTruthy();
    const row = screen.getByText("Library").closest("tr")!;
    expect(row.textContent).toContain("7 · 4");
    expect(row.textContent).toContain("40");
    await fireEvent.click(screen.getByText("Files"));
    expect(screen.getByText("2026/a.jpg")).toBeTruthy();
    expect(screen.getByText("Deleted · 1 of 3")).toBeTruthy();
    // A restic destination is named with why it has no table.
    expect(screen.getByText("restic destinations are not previewed")).toBeTruthy();
    expect(screen.getAllByRole("table").length).toBe(1);
  });

  it("shows the comparison while it runs, and keeps what a destination would leave", async () => {
    const running: Preview = {
      ...done, result: "running", finished_at: null,
      progress: { destination: "Bucket", folder: "Library", files_done: 1200, files_total: 0, bytes_done: 0, bytes_total: 0, speed: 0 },
      items: [item({ result: "running" }), item({ label: "Dump", path: "/pool/dump", extra: 5 })],
    };
    vi.spyOn(api, "GET").mockResolvedValue(answer(running));
    mount({ has: true, previewing: true });
    await waitFor(() => expect(screen.getByText("Comparing Library with Bucket")).toBeTruthy());
    expect(screen.getByText("1200 files compared")).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "Kept" })).toBeTruthy();
  });
});
