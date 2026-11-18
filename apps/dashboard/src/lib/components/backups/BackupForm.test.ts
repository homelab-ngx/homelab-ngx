import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client.ts";
import type { Coverage, Destination } from "../../backups.ts";
import BackupForm from "./BackupForm.svelte";

const coverage: Coverage = {
  uncovered: 1,
  stacks: [
    {
      stack: "immich",
      prepare: "",
      folders: [
        { key: "UPLOAD_DIR", label: "Library", host_path: "/pool/immich", excludes: [{ pattern: "/thumbs/**", reason: "rebuilt" }], problem: "", covered_by: ["Other"] },
        { key: "/var/backup", label: "Database dumps", host_path: "/cache/appdata/stacks/immich/root/var/backup", excludes: [], problem: "", covered_by: [] },
      ],
    },
  ],
};
const destinations = [
  { id: 3, name: "Backblaze", kind: "s3", password_confirmed: false, used_by: [] },
  { id: 4, name: "Disk", kind: "restic", password_confirmed: false, used_by: [] },
] as unknown as Destination[];

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("BackupForm", () => {
  it("creates a backup from the ticked folders, the server settings and a destination", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue({ data: {}, response: new Response() } as never);
    const onSaved = vi.fn();
    const onConfirmPassword = vi.fn();
    render(BackupForm, {
      props: { backup: null, coverage, destinations, preselect: [{ stack: "immich", key: "/var/backup" }], onSaved, onClose: () => {}, onConfirmPassword },
    });

    expect(screen.getByText(/also in Other/)).toBeTruthy();
    expect((screen.getByLabelText(/Database dumps/) as HTMLInputElement).checked).toBe(true);
    const save = screen.getByRole("button", { name: "Create backup" }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);

    await fireEvent.input(screen.getByLabelText("Name"), { target: { value: "Everything" } });
    await fireEvent.click(screen.getByLabelText(/^Server settings/));
    await fireEvent.click(screen.getByLabelText(/Backblaze/));
    // A restic destination whose password is not saved offers the way to save it.
    await fireEvent.click(screen.getByRole("button", { name: "Save password…" }));
    expect(onConfirmPassword).toHaveBeenCalledWith(destinations[1]);

    expect(save.disabled).toBe(false);
    await fireEvent.click(save);
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(post).toHaveBeenCalledWith("/backups", {
      body: {
        name: "Everything",
        enabled: true,
        schedule: "0 4 * * *",
        bwlimit: "",
        transfers: 0,
        healthcheck_url: "",
        destination_ids: [3],
        sources: [
          { kind: "core", excludes: [], size_only: false },
          { kind: "stack", stack: "immich", folder: "/var/backup", excludes: [], size_only: false },
        ],
      },
    });
  });

  it("selects every folder that can be found, per stack and for all, and posts what is left ticked", async () => {
    const wide: Coverage = {
      uncovered: 4,
      stacks: [
        ...coverage.stacks,
        {
          stack: "media",
          prepare: "",
          folders: [
            { key: "/cache/appdata", label: "App settings", host_path: "/x/appdata", excludes: [], problem: "", covered_by: [] },
            { key: "/var/lib/qui", label: "qui settings", host_path: "/x/qui", excludes: [], problem: "", covered_by: [] },
            { key: "MISSING_DIR", label: "Lost folder", host_path: "", excludes: [], problem: "the variable has no value", covered_by: [] },
          ],
        },
      ],
    };
    const post = vi.spyOn(api, "POST").mockResolvedValue({ data: {}, response: new Response() } as never);
    const onSaved = vi.fn();
    render(BackupForm, { props: { backup: null, coverage: wide, destinations, onSaved, onClose: () => {}, onConfirmPassword: () => {} } });
    const box = (name: RegExp) => screen.getByLabelText(name) as HTMLInputElement;

    // One stack.
    await fireEvent.click(screen.getByRole("button", { name: "Select all folders of media" }));
    expect(box(/App settings/).checked && box(/qui settings/).checked).toBe(true);
    expect(box(/Lost folder/).checked).toBe(false);
    expect(box(/Lost folder/).disabled).toBe(true);
    expect(box(/Library/).checked).toBe(false);
    await fireEvent.click(screen.getByRole("button", { name: "Deselect all folders of media" }));
    expect(box(/App settings/).checked).toBe(false);

    // Everything, then one off again.
    await fireEvent.click(screen.getByRole("button", { name: "Select all recommended folders" }));
    expect(screen.getByRole("button", { name: "Deselect all recommended folders" })).toBeTruthy();
    expect(box(/^Server settings/).checked).toBe(false);
    await fireEvent.click(box(/qui settings/));
    expect(screen.getByRole("button", { name: "Select all recommended folders" })).toBeTruthy();

    await fireEvent.input(screen.getByLabelText("Name"), { target: { value: "All of it" } });
    await fireEvent.click(screen.getByLabelText(/Backblaze/));
    await fireEvent.click(screen.getByRole("button", { name: "Create backup" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    const sent = post.mock.calls.at(-1)?.[1] as unknown as { body: { sources: { stack?: string; folder?: string }[] } };
    expect(sent.body.sources.map((s) => `${s.stack}:${s.folder}`)).toEqual(["immich:UPLOAD_DIR", "immich:/var/backup", "media:/cache/appdata"]);
  });
});
