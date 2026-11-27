import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Stack } from "#lib/api/client.ts";
import type { HostSettings, TunnelStatus } from "#lib/host.ts";
import TunnelRoutes from "./TunnelRoutes.svelte";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const domain: HostSettings["domain"] = {
  domain: "home.example",
  dashboard_label: "homelab",
  acme_email: "",
  dns_provider: "cloudflare",
  dns_credentials_set: true,
};

const tunnel: HostSettings["tunnel"] = {
  enabled: true,
  name: "homelab",
  zone: "example.net",
  tunnel_id: "6f1c0000-0000-0000-0000-00000000e2a0",
  credentials_set: true,
  dns_managed: true,
  routes: [
    { hostname: "photos.example.net", target: "photos" },
    { hostname: "books.example.net", target: "calibre", path: "^/opds" },
  ],
};

const status: TunnelStatus = {
  state: "connected",
  connections: 4,
  want: 4,
  colos: ["ams", "fra"],
  version: "2026.10.0",
  zone: "example.net",
  recent_errors: [],
  routes: [
    { hostname: "photos.example.net", target: "photos", public: false, dns: "added", protection: "protected" },
    { hostname: "books.example.net", target: "calibre", path: "^/opds", public: false, dns: "added", protection: "open" },
  ],
};

const stacks = [
  { name: "immich", label: "Photos", approved: { manifest: { routes: [{ host: "photos.home.example", port: 2283 }] } } },
  { name: "calibre", label: "Calibre", approved: { manifest: { routes: [{ host: "calibre.home.example", port: 8083 }] } } },
] as unknown as Stack[];

describe("TunnelRoutes", () => {
  it("renders clickable route links, states, and validates add-route modal against zone and dashboard", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue({
      data: { tunnel, domain } as HostSettings,
      response: new Response(null, { status: 200 }),
    } as never);
    const onSaved = vi.fn();
    const onRecheck = vi.fn().mockResolvedValue(undefined);

    render(TunnelRoutes, { props: { tunnel, domain, status, stacks, readOnly: false, onSaved, onRecheck } });

    expect(screen.getByRole("link", { name: "photos.example.net" }).getAttribute("href")).toBe("https://photos.example.net");
    expect(screen.getByText("protected")).toBeTruthy();
    expect(screen.getByText("open")).toBeTruthy();
    expect(screen.getByText(/1 route answers without Cloudflare Access/)).toBeTruthy();
    expect(screen.getByText("No Cloudflare Access")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Add a route" }));

    const hostInput = screen.getByLabelText("Public name");
    await fireEvent.input(hostInput, { target: { value: "status.other.org" } });
    expect(screen.getByText("Not in example.net.")).toBeTruthy();

    await fireEvent.input(hostInput, { target: { value: "status" } });
    const serves = screen.getByLabelText("Serves");
    await fireEvent.change(serves, { target: { value: "__other__" } });
    const custom = screen.getByLabelText("Private host name");
    await fireEvent.input(custom, { target: { value: "homelab" } });
    expect(screen.getByText("The dashboard cannot be published through the tunnel.")).toBeTruthy();

    await fireEvent.change(serves, { target: { value: "calibre" } });
    await fireEvent.click(screen.getByLabelText("Open to anyone"));
    await fireEvent.click(screen.getByRole("button", { name: "Add route" }));

    await vi.waitFor(() => expect(put).toHaveBeenCalled());
    const body = put.mock.calls[0]![1] as { body: { enabled: boolean; routes: Array<{ hostname: string; target: string; public?: boolean }> } };
    expect(body.body.routes[2]).toEqual({ hostname: "status.example.net", target: "calibre", public: true });
    expect(onSaved).toHaveBeenCalled();
  });

  it("shows remove confirmation noting the CNAME stays at Cloudflare", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue({
      data: { tunnel: { ...tunnel, routes: [tunnel.routes[1]!] }, domain } as HostSettings,
      response: new Response(null, { status: 200 }),
    } as never);
    const onSaved = vi.fn();
    render(TunnelRoutes, { props: { tunnel, domain, status, stacks, readOnly: false, onSaved, onRecheck: vi.fn() } });

    await fireEvent.click(screen.getByRole("button", { name: "More actions for photos.example.net" }));
    await fireEvent.click(await screen.findByRole("menuitem", { name: "Remove…" }));

    expect(screen.getByText(/DNS CNAME record stays at Cloudflare/)).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Remove" }));

    await vi.waitFor(() => expect(put).toHaveBeenCalled());
    const body = put.mock.calls[0]![1] as { body: { routes: Array<{ hostname: string }> } };
    expect(body.body.routes.map((r) => r.hostname)).toEqual(["books.example.net"]);
    expect(onSaved).toHaveBeenCalled();
  });

  it("marks an open route public in one click and allows unmarking from the menu", async () => {
    const put = vi.spyOn(api, "PUT").mockResolvedValue({
      data: { tunnel, domain } as HostSettings,
      response: new Response(null, { status: 200 }),
    } as never);
    const onSaved = vi.fn();

    render(TunnelRoutes, { props: { tunnel, domain, status, stacks, readOnly: false, onSaved, onRecheck: vi.fn() } });

    await fireEvent.click(screen.getByRole("button", { name: "Mark public" }));
    await vi.waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const markBody = put.mock.calls[0]![1] as { body: { enabled: boolean; routes: Array<{ hostname: string; public?: boolean }> } };
    expect(markBody.body.routes).toEqual([
      { hostname: "photos.example.net", target: "photos" },
      { hostname: "books.example.net", target: "calibre", path: "^/opds", public: true },
    ]);
    expect(onSaved).toHaveBeenCalledTimes(1);

    cleanup();
    const publicTunnel: HostSettings["tunnel"] = {
      ...tunnel,
      routes: [
        { hostname: "photos.example.net", target: "photos" },
        { hostname: "books.example.net", target: "calibre", path: "^/opds", public: true },
      ],
    };
    const publicStatus: TunnelStatus = {
      ...status,
      routes: [
        { hostname: "photos.example.net", target: "photos", public: false, dns: "added", protection: "protected" },
        { hostname: "books.example.net", target: "calibre", path: "^/opds", public: true, dns: "added", protection: "public" },
      ],
    };
    render(TunnelRoutes, {
      props: { tunnel: publicTunnel, domain, status: publicStatus, stacks, readOnly: false, onSaved, onRecheck: vi.fn() },
    });

    expect(screen.queryByText(/without Cloudflare Access/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Mark public" })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: "More actions for books.example.net" }));
    await fireEvent.click(await screen.findByRole("menuitem", { name: "Unmark public" }));
    await vi.waitFor(() => expect(put).toHaveBeenCalledTimes(2));
    const unmarkBody = put.mock.calls[1]![1] as { body: { routes: Array<{ hostname: string; public?: boolean }> } };
    expect(unmarkBody.body.routes[1]).toEqual({ hostname: "books.example.net", target: "calibre", path: "^/opds" });
  });
});
