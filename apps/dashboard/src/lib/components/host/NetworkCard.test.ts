import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
import type { HostSettings } from "#lib/host.ts";
import NetworkCard from "./NetworkCard.svelte";

const settings = (lan: string[]) =>
  ({
    network: { lan, ipv4: { mode: "dhcp", dns: [] } },
    wired: [{ name: "enp3s0", mac: "02:00:5e:00:53:10", addresses: ["192.0.2.10"], subnets: ["192.0.2.0/24"] }],
  }) as unknown as HostSettings;

afterEach(cleanup);

describe("NetworkCard", () => {
  it("offers the wired subnet while no LAN is set", async () => {
    render(NetworkCard, { props: { settings: settings([]), readOnly: false, onSaved: () => {} } });
    expect(screen.getByText(/open nothing until it's set/)).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Use 192.0.2.0/24" }));
    expect((screen.getByLabelText("LAN subnets") as HTMLInputElement).value).toBe("192.0.2.0/24");
    expect(screen.queryByRole("button", { name: /^Use / })).toBeNull();
  });

  it("says nothing once a LAN is set", () => {
    render(NetworkCard, { props: { settings: settings(["10.0.0.0/24"]), readOnly: false, onSaved: () => {} } });
    expect(screen.queryByText(/open nothing/)).toBeNull();
  });
});
