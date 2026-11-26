import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Stack } from "#lib/api/client.ts";
import Harness from "./AddHarness.test.svelte";

vi.mock("$app/navigation", () => ({ goto: vi.fn(async () => {}) }));
vi.mock("$app/state", () => ({ page: { url: new URL("http://localhost/stacks/add") } }));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const addedStack = (name: string): Stack =>
  ({
    name,
    label: "",
    address: "",
    source: `github:alice/${name}`,
    source_dirty: false,
    created_at: "2026-11-26T00:00:00Z",
    fetched: {
      at: "2026-11-26T00:00:00Z",
      rev: "abcdef1234567890",
      url: `git+ssh://git@github.com/alice/${name}.git?ref=main&rev=abcdef1234567890`,
      manifest: {
        name,
        version: 7,
        containers: false,
        gpu: false,
        egress: [],
        ports: [],
        routes: [],
        storage: [],
        health: { units: [name] },
      },
    },
    commits: [],
    generation: 0,
    generations: [],
    new_grants: [],
    restart_needed: false,
    idle_grants: [],
    running: false,
    want_running: false,
    autostart: true,
    frozen: false,
    update_state: "up_to_date",
    folders: [],
    variables: [],
    variables_ready: true,
  }) as unknown as Stack;

describe("stacks/add", () => {
  it("shows nothing about SSH keys when adding a public repository", async () => {
    const post = vi
      .spyOn(api, "POST")
      .mockResolvedValue({ data: addedStack("public-app"), response: new Response(null, { status: 201 }) } as never);

    render(Harness, { props: { client: new QueryClient() } });
    await fireEvent.input(screen.getByPlaceholderText("https://github.com/owner/homelab-paperless"), {
      target: { value: "https://github.com/alice/public-app" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Read the repo" }));

    await vi.waitFor(() => expect(screen.getByRole("button", { name: "Approve and install" })).toBeTruthy());
    expect(post).toHaveBeenCalledWith("/stacks", { body: { source: "github:alice/public-app", name: undefined } });
    expect(screen.queryByText("Deploy key needed")).toBeNull();
  });

  it("shows the deploy key and link on private_repo, then retries to install", async () => {
    const pub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPrivateRepoKey homelab-ngx";
    const post = vi
      .spyOn(api, "POST")
      .mockResolvedValueOnce({
        error: {
          code: "private_repo",
          detail: "cannot access private repository",
          public_key: pub,
          fingerprint: "SHA256:privfp789",
          deploy_key_url: "https://github.com/alice/private-app/settings/keys/new",
          host_key: true,
        },
        response: new Response(null, { status: 403 }),
      } as never)
      .mockResolvedValueOnce({
        data: addedStack("private-app"),
        response: new Response(null, { status: 201 }),
      } as never);

    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });

    render(Harness, { props: { client: new QueryClient() } });
    await fireEvent.input(screen.getByPlaceholderText("https://github.com/owner/homelab-paperless"), {
      target: { value: "https://github.com/alice/private-app" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Read the repo" }));

    await vi.waitFor(() => expect(screen.getByText("Deploy key needed")).toBeTruthy());
    expect(screen.getByText(pub)).toBeTruthy();
    expect(screen.getByText("SHA256:privfp789")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Open deploy keys" }).getAttribute("href")).toBe(
      "https://github.com/alice/private-app/settings/keys/new",
    );

    await fireEvent.click(screen.getByRole("button", { name: "Copy key" }));
    expect(writeText).toHaveBeenCalledWith(pub);

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await vi.waitFor(() => expect(screen.getByRole("button", { name: "Approve and install" })).toBeTruthy());
    expect(post).toHaveBeenCalledTimes(2);
  });
});
