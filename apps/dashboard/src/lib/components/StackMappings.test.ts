import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Stack } from "../api/client.ts";
import type { StackVariable } from "../variables.ts";
import Harness from "./StackMappingsHarness.test.svelte";

const variable = (name: string, over: Partial<StackVariable> = {}): StackVariable => ({
  name,
  description: "",
  declared: false,
  secret: false,
  set: true,
  in_env: true,
  kind: "env",
  required: false,
  source: "stored",
  ...over,
});

const stack = (variables: StackVariable[], folders: unknown[] = []): Stack => ({ name: "media", running: true, variables, folders }) as unknown as Stack;
const music = { path: "/var/mnt/pool/music", mount: "/music", read_only: true, updated_at: "2026-11-22T12:00:00Z" };

afterEach(cleanup);

describe("StackMappings", () => {
  it("lists declared secrets write-only and the operator's own plain values", () => {
    render(Harness, {
      props: {
        client: new QueryClient(),
        stack: stack([
          variable("VPN_KEY", { declared: true, kind: "secret", secret: true, required: true, set: false, in_env: false, source: "unset", description: "WireGuard key" }),
          variable("TZ", { value: "Europe/Stockholm" }),
          variable("ODD", { value: "a b ", in_env: false }),
        ]),
      },
    });
    // The description sits behind its label; every row carries one state word.
    expect(screen.queryByText("WireGuard key")).toBeNull();
    expect(screen.getByRole("button", { name: "VPN_KEY" })).toBeTruthy();
    expect(screen.getAllByText("required")).toHaveLength(1);
    expect(screen.getAllByText("set")).toHaveLength(2);
    expect((screen.getByLabelText("New value for VPN_KEY") as HTMLInputElement).type).toBe("password");
    expect((screen.getByLabelText("Value of TZ") as HTMLInputElement).value).toBe("Europe/Stockholm");
    // Only the set value that is not env-safe has a note (file only) behind its label.
    expect(screen.getByRole("button", { name: "ODD" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "TZ" })).toBeNull();
    // Each row's rarer actions are in its menu: Generate for the secret, Remove for the operator's own.
    expect(screen.getAllByRole("button", { name: /^More actions for/ })).toHaveLength(3);
  });

  it("fills a generated key into a declared secret", async () => {
    render(Harness, { props: { client: new QueryClient(), stack: stack([variable("KEY", { declared: true, kind: "secret", secret: true, set: false, source: "unset" })]) } });
    await fireEvent.keyDown(screen.getByRole("button", { name: "More actions for KEY" }), { key: "Enter" });
    await fireEvent.click(await screen.findByRole("menuitem", { name: "Generate" }));
    expect((screen.getByLabelText("New value for KEY") as HTMLInputElement).value).toMatch(/^[0-9a-f]{64}$/);
  });

  it("refuses a bad or taken name in the Add form", async () => {
    render(Harness, { props: { client: new QueryClient(), stack: stack([variable("TZ", { value: "UTC" })]) } });
    await fireEvent.click(screen.getByRole("button", { name: "Add mapping" }));
    const name = screen.getByLabelText("New variable name");
    await fireEvent.input(screen.getByLabelText("New variable value"), { target: { value: "x" } });
    await fireEvent.input(name, { target: { value: "9lives" } });
    expect(screen.getByText("Start with a capital letter.")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Add" }) as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.input(name, { target: { value: "tz" } });
    expect(screen.getByText("TZ already exists.")).toBeTruthy();
    await fireEvent.input(name, { target: { value: "api_url" } });
    expect((name as HTMLInputElement).value).toBe("API_URL");
    expect((screen.getByRole("button", { name: "Add" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("puts values under Env mappings and folders under Path mappings, with their defaults and mounts", async () => {
    render(Harness, {
      props: {
        client: new QueryClient(),
        stack: stack([
          variable("MEDIA_DIR", { declared: true, kind: "path", required: true, set: true, value: "/pool/media", default: "/pool/media", mount: "/media", read_only: false, in_env: false }),
          variable("DOWNLOADS", { declared: true, kind: "path", required: true, set: false, source: "unset", mount: "/downloads", read_only: true, in_env: false }),
          variable("TZ", { declared: true, set: false, source: "default", default: "UTC" }),
          variable("API_KEY", { declared: true, kind: "secret", secret: true, required: true, in_env: true }),
        ]),
      },
    });
    for (const h of ["Env mappings", "Path mappings"]) expect(screen.getByRole("heading", { name: h })).toBeTruthy();
    // The mount is in the label's help; the row keeps one button, Browse, once it has a path.
    expect(screen.queryByText(/Mounts at/)).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "MEDIA_DIR" }));
    expect(screen.getByText(/Mounts at \/media · read-write/)).toBeTruthy();
    expect(screen.getAllByRole("link", { name: /^Browse/ })).toHaveLength(1);
    const media = screen.getByLabelText("Host path for MEDIA_DIR") as HTMLInputElement;
    expect(media.value).toBe("/pool/media");
    const tz = screen.getByLabelText("Value of TZ") as HTMLInputElement;
    expect(tz.value).toBe("");
    expect(tz.placeholder).toBe("UTC");
    // Only DOWNLOADS is required with no value; TZ runs on its default.
    expect(screen.getAllByText("required")).toHaveLength(1);
    expect(screen.getAllByText("default")).toHaveLength(1);
    expect(screen.getAllByText("set")).toHaveLength(2);
    // Required and unset rows have nothing to clear: the secret has a menu (Generate), and the set path (Make read-only).
    expect(screen.getAllByRole("button", { name: /^More actions for/ }).map((b) => b.getAttribute("aria-label"))).toEqual([
      "More actions for API_KEY",
      "More actions for MEDIA_DIR",
    ]);
  });

  it("lets an optional setting with a stored value be cleared", async () => {
    render(Harness, {
      props: { client: new QueryClient(), stack: stack([variable("LOG_LEVEL", { declared: true, value: "debug", default: "info" })]) },
    });
    await fireEvent.keyDown(screen.getByRole("button", { name: "More actions for LOG_LEVEL" }), { key: "Enter" });
    expect(await screen.findByRole("menuitem", { name: "Clear value…" })).toBeTruthy();
  });

  it("shows a refused path under its row", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue({
      error: { code: "invalid_path", detail: 'DIR: "/nope" is not on the host: stat /nope: no such file or directory' },
      response: new Response(null, { status: 422 }),
    } as never);
    try {
      render(Harness, {
        props: { client: new QueryClient(), stack: stack([variable("DIR", { declared: true, kind: "path", required: true, set: false, source: "unset", mount: "/data", in_env: false })]) },
      });
      expect((screen.getByRole("button", { name: "Apply" }) as HTMLButtonElement).disabled).toBe(true);
      await fireEvent.input(screen.getByLabelText("Host path for DIR"), { target: { value: "/nope" } });
      await fireEvent.click(screen.getByRole("button", { name: "Apply" }));
      expect((await screen.findByRole("alert")).textContent).toMatch(/^"\/nope" is not on the host/);
    } finally {
      put.mockRestore();
    }
  });

  it("picks an allowed value from a list, and says an unset one keeps the stack's own (version 7)", () => {
    const v7 = { ...stack([variable("LOG_LEVEL", { declared: true, set: false, source: "unset", enum: ["info", "debug"] }), variable("PORT", { declared: true, set: false, source: "unset", type: "number" })]), fetched: { manifest: { version: 7 } } } as unknown as Stack;
    render(Harness, { props: { client: new QueryClient(), stack: v7 } });
    const level = screen.getByLabelText("Value of LOG_LEVEL") as HTMLSelectElement;
    expect(level.tagName).toBe("SELECT");
    expect([...level.options].map((o) => o.value)).toEqual(["", "info", "debug"]);
    expect((screen.getByLabelText("Value of PORT") as HTMLInputElement).type).toBe("number");
    expect(level.options[0]!.textContent).toBe("The stack's own value");
    expect((screen.getByLabelText("Value of PORT") as HTMLInputElement).placeholder).toBe("The stack's own value");
  });

  it("shows the API token as filled in by the host, with nothing to edit", () => {
    render(Harness, {
      props: { client: new QueryClient(), stack: stack([variable("HOMELAB_API_TOKEN", { declared: true, builtin: true, kind: "secret", secret: true, set: true, in_env: false })]) },
    });
    expect(screen.getByText("issued")).toBeTruthy();
    expect(screen.getByText("host")).toBeTruthy();
    expect(screen.queryByLabelText("New value for HOMELAB_API_TOKEN")).toBeNull();
    expect(screen.queryByRole("button", { name: /^More actions for/ })).toBeNull();
  });

  it("narrows a writable path to read-only from its menu, and not one the stack binds read-only", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue({ data: stack([]), response: new Response(null, { status: 200 }) } as never);
    try {
      const path = (name: string, over: Partial<StackVariable>) =>
        variable(name, { declared: true, kind: "path", mount: "/data", in_env: false, value: "/var/mnt/pool/x", ...over });
      render(Harness, {
        props: { client: new QueryClient(), stack: stack([path("DATA", { read_only: false }), path("LIB", { read_only: true })]) },
      });
      await fireEvent.keyDown(screen.getByRole("button", { name: "More actions for LIB" }), { key: "Enter" });
      expect(screen.queryByRole("menuitem", { name: "Make read-only" })).toBeNull();
      await fireEvent.keyDown(screen.getByRole("button", { name: "More actions for DATA" }), { key: "Enter" });
      await fireEvent.click(await screen.findByRole("menuitem", { name: "Make read-only" }));
      expect(put).toHaveBeenCalledWith("/stacks/{name}/variables/{var}", {
        params: { path: { name: "media", var: "DATA" } },
        body: { value: "/var/mnt/pool/x", read_only: true },
      });
    } finally {
      put.mockRestore();
    }
  });

  it("lists added folders under Path mappings, and one the manifest overlaps as not bound", () => {
    render(Harness, {
      props: { client: new QueryClient(), stack: stack([], [music, { ...music, mount: "/data/x", path: "/var/mnt/pool/x", shadowed_by: "its path DATA" }]) },
    });
    expect(screen.getByText("read-only")).toBeTruthy();
    expect(screen.getByText("not bound")).toBeTruthy();
    expect(screen.getByRole("button", { name: "/data/x" })).toBeTruthy();
  });

  it("adds a path mapping read-only by default, with the mount filled from the folder, keeping the others", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue({ data: stack([]), response: new Response(null, { status: 200 }) } as never);
    try {
      render(Harness, { props: { client: new QueryClient(), stack: stack([], [music]) } });
      await fireEvent.click(screen.getByRole("button", { name: "Add mapping" }));
      await fireEvent.change(screen.getByLabelText("Mapping type"), { target: { value: "path" } });
      await fireEvent.input(screen.getByLabelText("Host folder"), { target: { value: "/var/mnt/pool/films/" } });
      expect((screen.getByLabelText("Mount in the stack") as HTMLInputElement).value).toBe("/mnt/films");
      await fireEvent.input(screen.getByLabelText("Mount in the stack"), { target: { value: "/films" } });
      await fireEvent.input(screen.getByLabelText("Host folder"), { target: { value: "/var/mnt/pool/films" } });
      expect((screen.getByLabelText("Mount in the stack") as HTMLInputElement).value).toBe("/films");
      await fireEvent.click(screen.getByRole("button", { name: "Add" }));
      expect(put).toHaveBeenCalledWith("/stacks/{name}/folders", {
        params: { path: { name: "media" } },
        body: {
          folders: [
            { path: music.path, mount: "/music", read_only: true },
            { path: "/var/mnt/pool/films", mount: "/films", read_only: true },
          ],
        },
      });
    } finally {
      put.mockRestore();
    }
  });

  const hinted = (folders: unknown[]): Stack =>
    ({
      ...stack([], folders),
      approved: { manifest: { added_folders: { under: "/shares", label: "Shares", description: "Each is one share.", read_only: false } } },
    }) as unknown as Stack;

  it("lists named folders under the manifest's label, by name", () => {
    render(Harness, {
      props: { client: new QueryClient(), stack: hinted([music, { ...music, mount: "/shares/photos", path: "/var/mnt/pool/photos", read_only: false }]) },
    });
    expect(screen.getByRole("heading", { name: "Shares" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "photos" })).toBeTruthy();
    // A folder mounted elsewhere keeps its mount.
    expect(screen.getByRole("button", { name: "/music" })).toBeTruthy();
  });

  it("adds a named folder under the manifest's directory, writable by its default", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue({ data: hinted([]), response: new Response(null, { status: 200 }) } as never);
    try {
      render(Harness, { props: { client: new QueryClient(), stack: hinted([]) } });
      await fireEvent.click(screen.getByRole("button", { name: "Add mapping" }));
      await fireEvent.change(screen.getByLabelText("Mapping type"), { target: { value: "path" } });
      await fireEvent.input(screen.getByLabelText("Host folder"), { target: { value: "/var/mnt/pool/photos" } });
      expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("photos");
      expect((screen.getByLabelText("Read-only") as HTMLInputElement).checked).toBe(false);
      await fireEvent.input(screen.getByLabelText("Name"), { target: { value: "a/b" } });
      expect((screen.getByRole("button", { name: "Add" }) as HTMLButtonElement).disabled).toBe(true);
      await fireEvent.input(screen.getByLabelText("Name"), { target: { value: "Photos" } });
      await fireEvent.click(screen.getByRole("button", { name: "Add" }));
      expect(put).toHaveBeenCalledWith("/stacks/{name}/folders", {
        params: { path: { name: "media" } },
        body: { folders: [{ path: "/var/mnt/pool/photos", mount: "/shares/Photos", read_only: false }] },
      });
    } finally {
      put.mockRestore();
    }
  });

  it("adds an env mapping as a secret", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue({ data: stack([]), response: new Response(null, { status: 200 }) } as never);
    try {
      render(Harness, { props: { client: new QueryClient(), stack: stack([]) } });
      await fireEvent.click(screen.getByRole("button", { name: "Add mapping" }));
      await fireEvent.input(screen.getByLabelText("New variable name"), { target: { value: "token" } });
      await fireEvent.input(screen.getByLabelText("New variable value"), { target: { value: "abc" } });
      await fireEvent.click(screen.getByLabelText("Secret"));
      await fireEvent.click(screen.getByRole("button", { name: "Add" }));
      expect(put).toHaveBeenCalledWith("/stacks/{name}/variables/{var}", { params: { path: { name: "media", var: "TOKEN" } }, body: { value: "abc", secret: true } });
    } finally {
      put.mockRestore();
    }
  });
});
