import { QueryClient } from "@tanstack/svelte-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Schema } from "../../api/client.ts";
import Harness from "./SubvolumeProfileHarness.test.svelte";

const TB = 1e12;
const GB = 1e9;
const done = { data: undefined, response: new Response(null, { status: 204 }) } as never;

const disk = (name: string, kind: "ssd" | "hdd", bcachefs_label: string, used?: number) =>
  ({
    name,
    path: `/dev/${name}`,
    kind,
    bcachefs_label,
    size: kind === "ssd" ? TB : 4 * TB,
    model: "Disk",
    friendly_name: "Disk",
    state: "rw",
    ...(used !== undefined ? { usage: { size_bytes: kind === "ssd" ? TB : 4 * TB, used_bytes: used, used_percent: Math.round((used / (kind === "ssd" ? TB : 4 * TB)) * 100) } } : {}),
  }) as Schema<"Disk">;

const tieredPool = (over: Partial<Schema<"Pool">> = {}): Schema<"Pool"> =>
  ({
    uuid: "u-1",
    name: "pool",
    label: "pool",
    state: "mounted",
    mountdir: "/var/mnt/pool",
    mount_name: "pool",
    auto_mount: true,
    data_replicas: 2,
    metadata_replicas: 2,
    foreground_target: "ssd",
    promote_target: "ssd",
    background_target: "hdd",
    metadata_target: "ssd",
    disks: [disk("sda", "hdd", "hdd.hdd1", 1.2 * TB), disk("sdb", "ssd", "ssd.ssd1", 920 * GB), disk("sdc", "hdd", "hdd.hdd2", 1.1 * TB)],
    usage: {
      total_bytes: 9 * TB,
      used_bytes: 2.3 * TB,
      available_bytes: 6.7 * TB,
      used_percent: 25.5,
      cached_bytes: 846 * GB,
      reconcile_metadata_bytes: 4.89 * GB,
    },
    ...over,
  }) as Schema<"Pool">;

afterEach(cleanup);

describe("SubvolumeProfileDialog", () => {
  it("saves profile and copy changes, warning on 1 copy and SSD spill", async () => {
    const patch = vi.spyOn(api, "PATCH").mockResolvedValue(done);
    const onClose = vi.fn();
    try {
      render(Harness, {
        props: {
          client: new QueryClient(),
          pool: tieredPool(),
          subvolume: { id: "256", path: "/var/mnt/pool/media", used_bytes: -1, profile: "cached" },
          onClose,
        },
      });

      const save = screen.getByRole("button", { name: "Save" });
      expect(save).toHaveProperty("disabled", true);

      await fireEvent.click(screen.getByRole("radio", { name: /Hot/ }));
      expect(screen.getByText("Extra copies spill to HDD.")).toBeTruthy();

      await fireEvent.click(screen.getByRole("radio", { name: /^1$/ }));
      expect(screen.getByText("No disk redundancy for this subvolume.")).toBeTruthy();
      expect(screen.queryByText("Extra copies spill to HDD.")).toBeNull();

      await fireEvent.click(save);
      await vi.waitFor(() => expect(onClose).toHaveBeenCalled());
      expect(patch).toHaveBeenCalledWith("/storage/subvolume", {
        body: { path: "/var/mnt/pool/media", profile: "hot", copies: 1 },
      });
    } finally {
      patch.mockRestore();
    }
  });

  it("hides placement on untiered pools and preserves custom targets when changing copies", async () => {
    const patch = vi.spyOn(api, "PATCH").mockResolvedValue(done);
    const onClose = vi.fn();
    try {
      render(Harness, {
        props: {
          client: new QueryClient(),
          pool: tieredPool(),
          subvolume: { id: "257", path: "/var/mnt/pool/custom", used_bytes: -1, profile: "custom", data_replicas: 1 },
          onClose,
        },
      });
      expect(screen.getByText("Custom bcachefs targets are set.")).toBeTruthy();

      await fireEvent.click(screen.getByRole("radio", { name: /2\s*\(default\)/ }));
      await fireEvent.click(screen.getByRole("button", { name: "Save" }));
      await vi.waitFor(() => expect(onClose).toHaveBeenCalled());
      expect(patch).toHaveBeenCalledWith("/storage/subvolume", {
        body: { path: "/var/mnt/pool/custom", copies: 0 },
      });
    } finally {
      patch.mockRestore();
    }

    cleanup();
    const untiered = tieredPool({
      foreground_target: "",
      promote_target: "",
      background_target: "",
      metadata_target: "",
      disks: [disk("nvme0n1", "ssd", "nvme.a"), disk("nvme1n1", "ssd", "nvme.b")],
    });
    render(Harness, {
      props: {
        client: new QueryClient(),
        pool: untiered,
        subvolume: { id: "258", path: "/var/mnt/pool/db", used_bytes: -1 },
      },
    });
    expect(screen.queryByRole("radio", { name: /Hot/ })).toBeNull();
    expect(screen.getByRole("radio", { name: /^1$/ })).toBeTruthy();
  });
});

describe("PoolBox advanced cache, disk usage, and reconcile stats", () => {
  it("shows per-disk usage, labels, cached bytes, and pending reconcile note in advanced view", () => {
    render(Harness, {
      props: {
        client: new QueryClient(),
        pool: tieredPool(),
        advanced: true,
      },
    });

    expect(screen.getByText("hdd.hdd1")).toBeTruthy();
    expect(screen.getByText("ssd.ssd1")).toBeTruthy();
    expect(screen.getByText(/920 GB/)).toBeTruthy();
    expect(screen.getByText("(92%)")).toBeTruthy();
    expect(screen.getByText("846 GB cached")).toBeTruthy();
    expect(screen.getByText("4.9 GB metadata")).toBeTruthy();
    expect(screen.getByText("(2× on 1 SSD)")).toBeTruthy();
  });
});
