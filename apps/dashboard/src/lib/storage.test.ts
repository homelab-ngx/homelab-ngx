import { describe, expect, it } from "vitest";
import type { Schema, Stack } from "./api/client.ts";
import { browseDefault, browseHref, browseRows, browseSort, capacity, crumbs, diskState, disksSummary, folderName, foldersOf, measuredAt, role, segments, systemDisk, usedBy } from "./storage.ts";

type Pool = Schema<"Pool">;
type Disk = Schema<"Disk">;

const TB = 1e12;

const pool = (over: Partial<Pool> = {}): Pool =>
  ({
    uuid: "u",
    name: "pool",
    state: "mounted",
    mountdir: "/pool",
    mount_name: "pool",
    data_replicas: 2,
    metadata_replicas: 2,
    foreground_target: "ssd",
    background_target: "hdd",
    promote_target: "ssd",
    metadata_target: "ssd",
    usage: { total_bytes: 12 * TB, used_bytes: 3 * TB, available_bytes: 8 * TB, used_percent: 25 },
    disks: [],
    ...over,
  }) as Pool;

const disk = (label: string, health?: Partial<NonNullable<Disk["health"]>>): Disk =>
  ({
    name: "sdx",
    path: "/dev/sdx",
    bcachefs_label: label,
    ...(health ? { health: { passed: true, temperature_c: 30, power_on_hours: 1, bad_sectors: 0, checked_at: "", ...health } } : {}),
  }) as Disk;

const sub = (path: string, used_bytes = -1, measured_at?: string) => ({ id: path, path, used_bytes, measured_at });

describe("capacity", () => {
  it("divides raw space by the number of copies", () => {
    expect(capacity(pool())).toEqual({ used: 1.5 * TB, total: 6 * TB, free: 4 * TB, percent: 25 });
  });
  it("is null without usage", () => {
    expect(capacity(pool({ usage: undefined }))).toBeNull();
  });
});

describe("role", () => {
  it("maps label groups to the pool's targets", () => {
    expect(role(pool(), disk("ssd.ssd1"))).toBe("cache");
    expect(role(pool(), disk("hdd.hdd2"))).toBe("storage");
    expect(role(pool({ background_target: "" }), disk("hdd.hdd2"))).toBe("");
  });
});

describe("disk health", () => {
  it("ranks failing over bad sectors over healthy", () => {
    expect(diskState(disk("x"))).toEqual({ tone: "idle", word: "unchecked" });
    expect(diskState(disk("x", {}))).toEqual({ tone: "ok", word: "healthy" });
    expect(diskState(disk("x", { bad_sectors: 3 }))).toEqual({ tone: "warn", word: "bad sectors" });
    expect(diskState(disk("x", { passed: false, bad_sectors: 3 }))).toEqual({ tone: "bad", word: "failing" });
  });
  it("summarises the worst first", () => {
    expect(disksSummary([disk("a", {}), disk("b", {})]).text).toBe("all healthy");
    expect(disksSummary([disk("a", { bad_sectors: 1 }), disk("b", { passed: false })]).text).toBe("1 disk failing");
    expect(disksSummary([disk("a"), disk("b", {})]).text).toBe("");
  });
});

describe("folders", () => {
  it("lists only the mounted pool's folders", () => {
    const subs = [sub("/pool/media"), sub("/other/x")];
    expect(foldersOf(pool(), subs).map((s) => s.path)).toEqual(["/pool/media"]);
    expect(foldersOf(pool({ state: "unmounted" }), subs)).toEqual([]);
  });

  it("finds the stacks whose path variables point inside", () => {
    const stack = (name: string, value: string) =>
      ({ name, variables: [{ name: "DIR", kind: "path", value }, { name: "X", kind: "env", value: "/pool/media" }] }) as unknown as Stack;
    const stacks = [stack("media", "/pool/media"), stack("email", "/pool/mail-archive/eml"), stack("other", "/pool/mediaX")];
    expect(usedBy("/pool/media", stacks)).toEqual(["media"]);
    expect(usedBy("/pool/mail-archive", stacks)).toEqual(["email"]);
    expect(usedBy("/pool/tmp", stacks)).toEqual([]);
  });

  it("splits the bar by the biggest folders", () => {
    const folders = [sub("/pool/a", 3 * TB), sub("/pool/b", TB), sub("/pool/c", TB), sub("/pool/d", TB), sub("/pool/e", -1)];
    const segs = segments(folders, 10 * TB, "/pool", 10 * TB, 2);
    expect(segs.map((s) => [s.label, s.percent])).toEqual([
      ["a", 30],
      ["b", 10],
      ["other", 20],
    ]);
  });

  it("never shows more than the pool's used share", () => {
    const segs = segments([sub("/pool/a", 3 * TB), sub("/pool/b", 2 * TB)], 10 * TB, "/pool", 4 * TB);
    expect(segs.reduce((n, s) => n + s.percent, 0)).toBeCloseTo(40);
    expect(segs[0]!.percent).toBeCloseTo(24);
  });

  it("reports the oldest measurement", () => {
    expect(measuredAt([sub("/pool/a", 1, "2026-11-16T10:00:00Z"), sub("/pool/b", 1, "2026-11-16T08:00:00Z"), sub("/pool/c")])).toBe(
      "2026-11-16T08:00:00Z",
    );
    expect(measuredAt([sub("/pool/a")])).toBeNull();
  });

  it("names a folder by its place on the pool", () => {
    expect(folderName("/var/mnt/pool/media", "/var/mnt/pool")).toBe("media");
    expect(folderName("/var/mnt/pool/media/films", "/var/mnt/pool")).toBe("media/films");
    expect(folderName("/var/mnt/pooled/x", "/var/mnt/pool")).toBe("/var/mnt/pooled/x");
    expect(folderName("/srv/x", "")).toBe("/srv/x");
  });
});

describe("the file browser", () => {
  type Entry = Schema<"Entry">;
  const entry = (name: string, type: Entry["type"], size = 0): Entry => ({
    name,
    type,
    size,
    disk_bytes: size,
    modified_at: "2026-11-17T10:00:00Z",
    mode: "-rw-r--r--",
    owner: "root",
    group: "root",
  });

  it("splits a path into breadcrumbs from /", () => {
    expect(crumbs("/")).toEqual([{ label: "/", path: "/" }]);
    expect(crumbs("/pool/media")).toEqual([
      { label: "/", path: "/" },
      { label: "pool", path: "/pool" },
      { label: "media", path: "/pool/media" },
    ]);
  });

  it("encodes the path in the address", () => {
    expect(browseHref("/pool/my films & more")).toBe("/storage/browse?path=%2Fpool%2Fmy%20films%20%26%20more");
  });

  it("joins the figures to folders, folders on top, the heaviest first", () => {
    const entries = [entry("a.txt", "file", 5), entry("films", "dir"), entry("music", "dir"), entry("big.iso", "file", 900), entry("link", "symlink", 9)];
    const rows = browseRows(entries, {
      available: true,
      folders: { films: { total: 700, exclusive: 500, shared: 200 }, "a.txt": { total: 1, exclusive: 1, shared: 0 } },
    });
    expect(rows.map((r) => r.name)).toEqual(["films", "music", "big.iso", "a.txt", "link"]);
    expect(rows[0]!.usage?.exclusive).toBe(500);
    expect(rows[3]!.usage).toBeUndefined();
    expect(rows[1]!.bytes).toBeNull();
  });

  it("sorts by a column either way, folders still on top and empty values last", () => {
    const entries = [entry("b.txt", "file", 5), entry("zoo", "dir"), entry("art", "dir"), entry("a.iso", "file", 900), entry("link", "symlink", 9)];
    const figures = { available: true, folders: { zoo: { total: 700, exclusive: 500, shared: 200 } } };
    const order = (key: Parameters<typeof browseSort>[1], desc: boolean) => browseRows(entries, figures, { key, desc }).map((r) => r.name);
    expect(order("name", false)).toEqual(["art", "zoo", "a.iso", "b.txt", "link"]);
    expect(order("name", true)).toEqual(["zoo", "art", "link", "b.txt", "a.iso"]);
    expect(order("size", false)).toEqual(["zoo", "art", "b.txt", "a.iso", "link"]);
    expect(order("exclusive", true)).toEqual(["zoo", "art", "a.iso", "b.txt", "link"]);
  });

  it("flips a column that is picked again and starts sizes from the top", () => {
    expect(browseSort(browseDefault, "size")).toEqual({ key: "size", desc: false });
    expect(browseSort(browseDefault, "name")).toEqual({ key: "name", desc: false });
    expect(browseSort({ key: "name", desc: false }, "modified")).toEqual({ key: "modified", desc: true });
  });

  it("lists folders by name when there are no figures", () => {
    const entries = [entry("music", "dir"), entry("films", "dir"), entry("a.txt", "file", 5)];
    expect(browseRows(entries, { available: false, folders: {} }).map((r) => r.name)).toEqual(["films", "music", "a.txt"]);
    expect(browseRows(entries).map((r) => r.name)).toEqual(["films", "music", "a.txt"]);
  });
});

describe("the system disk", () => {
  const GB = 1e9;
  const store = (over: Partial<Parameters<typeof systemDisk>[0]> = {}) => ({
    used_bytes: 12 * GB, free_bytes: 60 * GB, total_bytes: 100 * GB, own_partition: false, freed_bytes: 0, collecting: false, ...over,
  });

  it("shows the Nix store's share of what is used", () => {
    const d = systemDisk(store())!;
    expect([d.used, d.free, d.percent]).toEqual([40 * GB, 60 * GB, 40]);
    expect(d.parts.map((p) => [p.label, p.bytes])).toEqual([["Nix store", 12 * GB], ["Other", 28 * GB]]);
  });

  it("is one part until the store is measured, and on a partition of the store's own", () => {
    expect(systemDisk(store({ used_bytes: -1 }))!.parts.map((p) => p.label)).toEqual(["Used"]);
    expect(systemDisk(store({ own_partition: true }))!.parts.map((p) => p.label)).toEqual(["Nix store"]);
  });

  it("is nothing when the disk could not be read", () => {
    expect(systemDisk(store({ total_bytes: 0 }))).toBeNull();
  });
});
