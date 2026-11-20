import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Schema } from "../../api/client.ts";
import Harness from "./PoolDialogsHarness.test.svelte";

const disk = (name: string, kind: "ssd" | "hdd", contents = "") =>
  ({ name, path: `/dev/${name}`, kind, contents, size: 4e12, model: "Disk", friendly_name: "Disk" }) as Schema<"Disk">;
const done = { data: {}, response: new Response(null, { status: 201 }) } as never;

afterEach(cleanup);

describe("CreatePoolDialog", () => {
  it("makes a pool from the ticked disks, with as many copies as they allow", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue(done);
    const onClose = vi.fn();
    try {
      render(Harness, { props: { client: new QueryClient(), spare: [disk("sdb", "hdd"), disk("sdc", "hdd"), disk("sda", "ssd")], onClose } });
      const create = screen.getByRole("button", { name: "Create pool" });
      await fireEvent.input(screen.getByLabelText("Name"), { target: { value: "vault" } });
      expect(create).toHaveProperty("disabled", true);
      expect(screen.getByRole("radio", { name: "2" })).toHaveProperty("disabled", true);

      await fireEvent.click(screen.getByRole("checkbox", { name: /sdb/ }));
      await fireEvent.click(screen.getByRole("checkbox", { name: /sda/ }));
      expect(screen.getByText("1 SSD caches for 1 HDD")).toBeTruthy();
      await fireEvent.click(screen.getByRole("radio", { name: "2" }));
      await fireEvent.click(create);
      await vi.waitFor(() => expect(onClose).toHaveBeenCalled());
      expect(post).toHaveBeenCalledWith("/storage/pools", { body: { name: "vault", disks: ["/dev/sdb", "/dev/sda"], copies: 2, confirm: undefined } });
    } finally {
      post.mockRestore();
    }
  });

  it("erases a disk with contents only once the name is typed again", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue(done);
    try {
      render(Harness, { props: { client: new QueryClient(), spare: [disk("sdg", "hdd", 'ext4 "Data"')], onClose: () => {} } });
      const create = screen.getByRole("button", { name: "Create pool" });
      await fireEvent.input(screen.getByLabelText("Name"), { target: { value: "vault" } });
      await fireEvent.click(screen.getByRole("checkbox", { name: /sdg/ }));
      expect(screen.getByText("Everything on sdg is erased.")).toBeTruthy();
      expect(create).toHaveProperty("disabled", true);
      await fireEvent.input(screen.getByLabelText("Type vault again"), { target: { value: "vault" } });
      await fireEvent.click(create);
      await vi.waitFor(() => expect(post).toHaveBeenCalled());
      expect(post).toHaveBeenCalledWith("/storage/pools", { body: { name: "vault", disks: ["/dev/sdg"], copies: 1, confirm: "vault" } });
    } finally {
      post.mockRestore();
    }
  });

  it("refuses a name that is taken before asking the daemon", async () => {
    render(Harness, { props: { client: new QueryClient(), spare: [disk("sdb", "hdd")], onClose: () => {} } });
    await fireEvent.input(screen.getByLabelText("Name"), { target: { value: "tank" } });
    await fireEvent.click(screen.getByRole("checkbox", { name: /sdb/ }));
    expect(screen.getByText("/var/mnt/tank is taken.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Create pool" })).toHaveProperty("disabled", true);
  });
});

describe("DeletePoolDialog", () => {
  const pool = { uuid: "u-1", name: "vault", disks: [disk("sdb", "hdd"), disk("sdc", "hdd")] } as Schema<"Pool">;

  it("deletes once the pool's name is typed", async () => {
    const del = vi.spyOn(api, "DELETE").mockResolvedValue(done);
    const onClose = vi.fn();
    try {
      render(Harness, { props: { client: new QueryClient(), pool, onClose } });
      const button = screen.getByRole("button", { name: "Delete pool" });
      await fireEvent.input(screen.getByLabelText("Type vault"), { target: { value: "vaul" } });
      expect(button).toHaveProperty("disabled", true);
      await fireEvent.input(screen.getByLabelText("Type vault"), { target: { value: "vault" } });
      await fireEvent.click(button);
      await vi.waitFor(() => expect(onClose).toHaveBeenCalled());
      expect(del).toHaveBeenCalledWith("/storage/pools/{uuid}", { params: { path: { uuid: "u-1" }, query: { confirm: "vault" } } });
    } finally {
      del.mockRestore();
    }
  });

  it("names the stacks that use the pool and offers no delete", () => {
    render(Harness, { props: { client: new QueryClient(), pool, users: ["Photos", "Media"], onClose: () => {} } });
    expect(screen.getByText("Used by Photos, Media. Move or remove those stacks first.")).toBeTruthy();
    expect(screen.queryByLabelText("Type vault")).toBeNull();
    expect(screen.getByRole("button", { name: "Delete pool" })).toHaveProperty("disabled", true);
  });
});
