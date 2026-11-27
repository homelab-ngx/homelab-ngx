import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { HostSettings, TailscaleStatus } from "#lib/host.ts";
import TailscaleCard from "./TailscaleCard.svelte";

afterEach(cleanup);

function makeSettings(more: Partial<HostSettings["tailscale"]> = {}): HostSettings {
  return {
    managed: "image",
    host: { hostname: "homelab", timezone: "UTC", ssh_keys: [] },
    domain: { domain: "", dashboard_label: "", acme_email: "", dns_provider: "", dns_credentials_set: false },
    network: { lan: ["192.168.1.0/24"], ipv4: { mode: "dhcp", dns: [] } },
    firewall: { openings: [] },
    wireguard: { enabled: false, listen_port: 51820, address: "", endpoint: "", peers: [] },
    tunnel: { enabled: false, credentials_set: false, dns_managed: false, routes: [] },
    tailscale: {
      enabled: true,
      advertise_routes: ["192.168.1.0/24"],
      exit_node: false,
      accept_dns: true,
      ssh: false,
      login_server: "",
      ...more,
    },
    ddns: { enabled: false, name: "" },
    smtp: { enabled: false, host: "", port: 587, tls: "starttls", username: "", password_set: false },
    time: { clock: "", ntp: [] },
    console: { keyboard: "us" },
    wired: [{ name: "enp6s0", mac: "d8:43:ae:00:11:22", addresses: ["10.0.20.2"], subnets: ["10.0.20.0/24"] }],
    time_sync: { synced: true },
    applied: { errors: [] },
  };
}

function makeStatus(more: Partial<TailscaleStatus> = {}): TailscaleStatus {
  return {
    state: "running",
    tailnet: "example.github",
    dns_name: "homelab.tail1234.ts.net",
    ips: ["100.101.2.3", "fd7a:115c:a1e0::1"],
    version: "1.104.7",
    tags: [],
    health: [],
    routes: [
      { prefix: "192.168.1.0/24", approved: true },
      { prefix: "10.0.20.0/24", approved: false },
    ],
    exit_node: "off",
    online_peers: 4,
    total_peers: 6,
    ...more,
  };
}

describe("TailscaleCard", () => {
  it("shows identity, per-route state, health warnings, and offers non-added wired subnets", async () => {
    const expSoon = new Date(Date.now() + 5 * 86_400_000).toISOString();
    render(TailscaleCard, {
      props: {
        settings: makeSettings(),
        readOnly: false,
        onSaved: () => {},
        status: makeStatus({
          key_expiry: expSoon,
          health: ["Tailscale could not reach the control server."],
        }),
        onLogin: vi.fn().mockResolvedValue(true),
      },
    });

    expect(screen.getByText("example.github")).toBeTruthy();
    expect(screen.getByText("homelab.tail1234.ts.net")).toBeTruthy();
    expect(screen.getByText("1.104.7")).toBeTruthy();
    expect(screen.getByText(/Key expires in 5 days/)).toBeTruthy();
    expect(screen.getByText("Tailscale could not reach the control server.")).toBeTruthy();
    expect(screen.getByText("approved")).toBeTruthy();

    // Quick-pick button skips 192.168.1.0/24 (already offered) and offers enp6s0's subnet.
    expect(screen.queryByRole("button", { name: /\+ 192\.168\.1\.0\/24/ })).toBeNull();
    const pick = screen.getByRole("button", { name: "+ 10.0.20.0/24 · enp6s0" });
    await fireEvent.click(pick);
    expect(screen.getByText("pending")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /\+ 10\.0\.20\.0\/24/ })).toBeNull();
  });

  it("blocks Apply with an invalid CIDR message", async () => {
    render(TailscaleCard, {
      props: {
        settings: makeSettings({ advertise_routes: [] }),
        readOnly: false,
        onSaved: () => {},
        status: makeStatus({ state: "needs-login", routes: [] }),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Add a subnet" }));
    const input = screen.getByLabelText("Subnet 1") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "not-a-cidr" } });

    expect(screen.getByText("Enter an IPv4 subnet such as 192.168.1.0/24.")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Apply" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("supports auth-key and custom login server in Other ways to log in", async () => {
    const onLogin = vi.fn().mockResolvedValue(true);
    render(TailscaleCard, {
      props: {
        settings: makeSettings({ advertise_routes: [] }),
        readOnly: false,
        onSaved: () => {},
        status: makeStatus({ state: "needs-login", ips: [], routes: [] }),
        onLogin,
      },
    });

    await fireEvent.click(screen.getByText("Other ways to log in"));
    await fireEvent.input(screen.getByLabelText("Auth key"), { target: { value: "tskey-auth-123" } });
    await fireEvent.input(screen.getByLabelText("Login server"), { target: { value: "https://hs.example.net" } });
    await fireEvent.click(screen.getByRole("button", { name: "Log in with key" }));

    expect(onLogin).toHaveBeenCalledWith({
      auth_key: "tskey-auth-123",
      login_server: "https://hs.example.net",
      force_reauth: true,
    });
    // Secret key clears after successful login.
    await vi.waitFor(() => expect((screen.getByLabelText("Auth key") as HTMLInputElement).value).toBe(""));
  });

  it("shows expired alert and re-auth CTA when node key expired", async () => {
    const onLogin = vi.fn().mockResolvedValue(true);
    render(TailscaleCard, {
      props: {
        settings: makeSettings(),
        readOnly: false,
        onSaved: () => {},
        status: makeStatus({ state: "expired" }),
        onLogin,
      },
    });

    expect(screen.getByText(/Node key expired/)).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Log in again" }));
    expect(onLogin).toHaveBeenCalledWith({ force_reauth: true });
  });

  it("shows re-auth link while running and formats tag prefix", () => {
    render(TailscaleCard, {
      props: {
        settings: makeSettings(),
        readOnly: false,
        onSaved: () => {},
        status: makeStatus({
          state: "running",
          auth_url: "https://login.tailscale.com/a/reauth",
          tags: ["tag:server"],
        }),
      },
    });

    const link = screen.getByRole("link", { name: "Open Tailscale login" });
    expect(link.getAttribute("href")).toBe("https://login.tailscale.com/a/reauth");
    expect(screen.getByText(/tagged: server/)).toBeTruthy();
    expect(screen.queryByText(/tagged: tag:server/)).toBeNull();
  });
});
