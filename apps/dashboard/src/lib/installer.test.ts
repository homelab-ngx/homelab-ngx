import { describe, expect, it } from "vitest";
import { diskSegments, installAddress, installProgress, largestFree, planSegments, type Disk, type Part, type PlanRow } from "./installer";

const GB = 1e9;
const MB = 1e6;
const part = (n: number, holds: Part["holds"], type: string, label: string, start: number, size: number, fs = ""): Part => ({
  number: n,
  name: `nvme0n1p${n}`,
  path: `/dev/nvme0n1p${n}`,
  start,
  size,
  type,
  holds,
  label,
  fs,
});
const ESP = "C12A7328-F81F-11D2-BA4B-00A0C93EC93B";
const windows: Disk = {
  name: "nvme0n1",
  path: "/dev/nvme0n1",
  model: "Samsung SSD 990 EVO 1TB",
  size: 1000 * GB,
  removable: false,
  bus: "",
  table: "gpt",
  in_use: false,
  partitions: [
    part(1, "esp", ESP, "EFI system partition", MB, 512 * MB, "vfat"),
    part(3, "windows", "EBD0A0A2-B9E5-4433-87C0-68B6B72699C7", "Windows", 600 * MB, 599 * GB, "ntfs"),
  ],
  free: [{ start: 600 * GB, size: 400 * GB - MB }],
  free_total: 400 * GB - MB,
  erase: { ok: true },
  beside: { ok: true },
};

describe("the installer's disk view", () => {
  it("shows partitions and free space in disk order, the largest free run first to select", () => {
    const segs = diskSegments(windows);
    expect(segs.map((s) => s.title)).toEqual(["EFI system partition", "Windows", "Free"]);
    expect(largestFree(segs)?.size).toBe(400 * GB - MB);
  });

  it("groups an installed system's partitions by what they are", () => {
    const disk: Disk = {
      ...windows,
      partitions: [
        part(1, "esp", ESP, "esp", MB, GB, "vfat"),
        part(2, "homelab", "4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709", "homelab_0.0.1", GB, 4 * GB, "erofs"),
        part(3, "homelab", "2C7357ED-EBD2-46D9-AEC1-23D437EC2BF5", "homelab_0.0.1", 5 * GB, 64 * MB),
        part(4, "empty", "4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709", "_empty", 6 * GB, 4 * GB),
        part(5, "empty", "2C7357ED-EBD2-46D9-AEC1-23D437EC2BF5", "_empty", 10 * GB, 64 * MB),
        part(6, "homelab", "073D2B22-9F7E-4817-8D45-890277F7DD55", "nix", 11 * GB, 32 * GB, "ext4"),
        part(7, "linux", "4D21B016-B534-45C2-A9FB-5C16E091FD2D", "var", 43 * GB, 900 * GB, "ext4"),
      ],
      free: [],
    };
    const segs = diskSegments(disk);
    expect(segs.map((s) => `${s.title} ${s.names}`)).toEqual([
      "esp nvme0n1p1",
      "System 0.0.1 nvme0n1p2–p3",
      "System, spare nvme0n1p4–p5",
      "Nix store nvme0n1p6",
      "Data nvme0n1p7",
    ]);
    expect(segs[1]?.size).toBe(4 * GB + 64 * MB);
    expect(segs[1]?.part).toBeUndefined();
    // A system's row can be deleted: it names both of its partitions.
    expect(segs[1]?.members).toEqual(["nvme0n1p2", "nvme0n1p3"]);
    expect(segs.find((x) => x.holds === "free")?.members ?? []).toEqual([]);
  });

  it("draws the disk after the install from repart's plan, Windows kept", () => {
    const rows: PlanRow[] = [
      { type: "esp", label: "esp", node: "/dev/nvme0n1p1", offset: MB, size: 512 * MB, activity: "unchanged" },
      { type: "ebd0a0a2-b9e5-4433-87c0-68b6b72699c7", label: "-", node: "/dev/nvme0n1p3", offset: 600 * MB, size: 599 * GB, activity: "unchanged" },
      { type: "root-x86-64", label: "homelab_0.0.1", node: "/dev/nvme0n1p4", offset: 600 * GB, size: 4 * GB, activity: "create" },
      { type: "root-x86-64-verity", label: "homelab_0.0.1", node: "/dev/nvme0n1p5", offset: 604 * GB, size: 64 * MB, activity: "create" },
      { type: "root-x86-64", label: "_empty", node: "/dev/nvme0n1p6", offset: 605 * GB, size: 4 * GB, activity: "create" },
      { type: "root-x86-64-verity", label: "_empty", node: "/dev/nvme0n1p7", offset: 609 * GB, size: 64 * MB, activity: "create" },
      { type: "073d2b22-9f7e-4817-8d45-890277f7dd55", label: "nix", node: "/dev/nvme0n1p8", offset: 610 * GB, size: 32 * GB, activity: "create" },
      { type: "var", label: "var", node: "/dev/nvme0n1p9", offset: 642 * GB, size: 357 * GB, activity: "create" },
    ];
    const segs = planSegments(windows, rows);
    expect(segs.map((s) => `${s.title}:${s.change}:${s.holds}`)).toEqual([
      "EFI system partition:kept:esp",
      "Windows:kept:windows",
      "System 0.0.1:new:homelab",
      "System, spare:new:homelab",
      "Nix store:new:homelab",
      "Data:new:homelab",
    ]);
  });
});

describe("the install's progress", () => {
  it("moves by stage from the daemon's step and the log's markers", () => {
    const at = (phase: string, log: string[]) => installProgress("installing", phase, log);
    expect(at("partition", ["Partitioning /dev/nvme0n1"])).toMatchObject({ stage: 1, label: "Partitioning" });
    expect(at("partition", ["Partitioning /dev/nvme0n1", "Copying in '/dev/sda2' (4G) on block level into future partition 1."]).stage).toBe(2);
    expect(at("partition", ["Copying in '/dev/sda2'", "Formatting future partition 5."]).stage).toBe(3);
    expect(at("boot", []).label).toBe("Installing the boot loader");
    expect(installProgress("installed", "finish", []).percent).toBe(100);
  });
});

describe("installAddress", () => {
  it("prefers a chosen static IPv4 address over the installer DHCP lease", () => {
    expect(
      installAddress({
        current: { mode: "dhcp", address: "192.0.2.40/24", gateway: "192.0.2.1", dns: [] },
        ipv4: { mode: "static", address: "192.168.1.20/24", gateway: "192.168.1.1", dns: ["192.168.1.1"] },
      }),
    ).toBe("192.168.1.20");
  });

  it("falls back to the current DHCP lease when DHCP was kept", () => {
    expect(
      installAddress({
        current: { mode: "dhcp", address: "192.0.2.40/24", gateway: "192.0.2.1", dns: [] },
        ipv4: { mode: "dhcp" },
      }),
    ).toBe("192.0.2.40");
  });

  it("returns undefined when neither static nor DHCP provides an address", () => {
    expect(installAddress({ current: { mode: "dhcp" }, ipv4: undefined })).toBeUndefined();
  });
});
