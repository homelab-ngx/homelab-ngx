import { describe, expect, it } from "vitest";
import type { Schema, Stack } from "./api/client.ts";
import { ancestors, browseDefault, browseHref, editHref, hostPathProblem, hostTree, joinPath, parentOf, relativeTo, browseRows, browseSort, capacity, crumbs, diskState, disksSummary, flaggedDisks, folderName, folderSuggestions, foldersOf, pathParts, trimSlash, maxCopies, measuredAt, folderGroups, poolBlocked, poolDiskState, poolsSummary, removal, removeBlocked, role, running, segmentColor, segments, swatchOf, systemDisk, tiering, usedBy, withContents } from "./storage.ts";

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

describe("making and changing a pool", () => {
  const d = (name: string, kind: "ssd" | "hdd", label = "", contents = "") =>
    ({ name, path: `/dev/${name}`, kind, bcachefs_label: label, contents }) as Schema<"Disk">;
  const tiered = {
    data_replicas: 2,
    metadata_replicas: 2,
    foreground_target: "ssd",
    promote_target: "ssd",
    background_target: "hdd",
    disks: [d("sda", "ssd", "ssd.ssd1"), d("sdb", "hdd", "hdd.hdd1"), d("sdc", "hdd", "hdd.hdd2")],
  } as Schema<"Pool">;

  it("says what a mix of disks does, and nothing for one kind", () => {
    expect(tiering([d("a", "ssd"), d("b", "hdd"), d("c", "hdd")])).toBe("1 SSD caches for 2 HDDs");
    expect(tiering([d("a", "ssd"), d("b", "ssd"), d("c", "hdd")])).toBe("2 SSDs cache for 1 HDD");
    expect(tiering([d("b", "hdd"), d("c", "hdd")])).toBe("");
  });

  it("offers a copy per disk, three at most", () => {
    expect([0, 1, 2, 3, 5].map(maxCopies)).toEqual([1, 1, 2, 3, 3]);
  });

  it("names the disks a pool would erase", () => {
    expect(withContents([d("a", "hdd"), d("b", "hdd", "", "ext4")]).map((x) => x.name)).toEqual(["b"]);
  });

  it("keeps the last cache disk, and as many disks as copies", () => {
    expect(removeBlocked(tiered, tiered.disks[0]!)).toBe("It is the pool's only cache disk.");
    expect(removeBlocked(tiered, tiered.disks[1]!)).toBe("");
    const two = { ...tiered, background_target: "", disks: tiered.disks.slice(1) };
    expect(removeBlocked(two, two.disks[0]!)).toBe("The pool keeps 2 copies and needs 2 disks.");
    const one = { ...two, data_replicas: 1, metadata_replicas: 1, disks: two.disks.slice(1) };
    expect(removeBlocked(one, one.disks[0]!)).toBe("It is the pool's only disk.");
  });

  it("says how far a removal is", () => {
    const op = { kind: "remove", disk: "/dev/sdc", phase: "moving", started_at: "", total_bytes: 4000, left_bytes: 1000 } as Schema<"PoolOperation">;
    const size = (n: number) => `${n} B`;
    expect(removal(op, size)).toEqual({ percent: 75, text: "Removing sdc · 3000 B of 4000 B moved · 75%" });
    expect(removal({ ...op, phase: "erasing", left_bytes: 0 }, size).text).toBe("Removing sdc · erasing…");
    expect(removal({ ...op, phase: "failed" }, size).text).toBe("Removing sdc failed");
    expect([running(op), running({ ...op, finished_at: "x" }), running(undefined)]).toEqual([true, false, false]);
  });
});

describe("path completion", () => {
  const entry = (name: string, type = "dir") => ({ name, type }) as Schema<"Entry">;

  it("splits a typed path into the folder to list and the name being typed", () => {
    expect(pathParts("")).toEqual({ parent: "/", fragment: "" });
    expect(pathParts("/")).toEqual({ parent: "/", fragment: "" });
    expect(pathParts("/var/mn")).toEqual({ parent: "/var/", fragment: "mn" });
    expect(pathParts("/var/mnt/")).toEqual({ parent: "/var/mnt/", fragment: "" });
  });

  it("suggests folders only, by prefix and ignoring case, hiding dot-folders until a dot is typed", () => {
    const list = [entry("Music"), entry("media"), entry("notes.txt", "file"), entry(".snapshots"), entry("films")];
    expect(folderSuggestions(list, "m")).toEqual(["media", "Music"]);
    expect(folderSuggestions(list, "")).toEqual(["films", "media", "Music"]);
    expect(folderSuggestions(list, ".")).toEqual([".snapshots"]);
    expect(folderSuggestions(list, "", 1)).toEqual(["films"]);
  });

  it("drops a trailing slash but keeps the root", () => {
    expect(trimSlash("/var/mnt/pool/")).toBe("/var/mnt/pool");
    expect(trimSlash("/var/mnt/pool")).toBe("/var/mnt/pool");
    expect(trimSlash("/")).toBe("/");
    expect(trimSlash("")).toBe("");
  });
});

describe("the host editor", () => {
  type Entry = Schema<"Entry">;
  const entry = (name: string, type: Entry["type"]): Entry => ({ name, type, size: 0, disk_bytes: 0, modified_at: "2026-11-17T10:00:00Z", mode: "", owner: "root", group: "root" });

  it("opens a file with its own folder as the list, or below a root it names", () => {
    expect(editHref("/srv/app/conf/a.yml")).toBe("/storage/edit?path=%2Fsrv%2Fapp%2Fconf%2Fa.yml");
    expect(editHref("/srv/app/conf/a.yml", "/srv/app/conf")).toBe("/storage/edit?path=%2Fsrv%2Fapp%2Fconf%2Fa.yml");
    expect(editHref("/srv/app/conf/a.yml", "/srv/app")).toBe("/storage/edit?path=%2Fsrv%2Fapp%2Fconf%2Fa.yml&root=%2Fsrv%2Fapp");
  });

  it("walks host paths", () => {
    expect(parentOf("/srv/app/a.yml")).toBe("/srv/app");
    expect(parentOf("/a")).toBe("/");
    expect(joinPath("/", "a")).toBe("/a");
    expect(joinPath("/srv/", "a/b")).toBe("/srv/a/b");
    expect(relativeTo("/srv", "/srv/a/b")).toBe("a/b");
    expect(relativeTo("/srv", "/srv")).toBeNull();
    expect(relativeTo("/srv", "/srvx/a")).toBeNull();
    expect(relativeTo("/", "/a")).toBe("a");
    expect(ancestors("a/b/c.txt")).toEqual(["a", "a/b"]);
    expect(ancestors("c.txt")).toEqual([]);
  });

  it("lists folders first and shows only the open ones' insides", () => {
    const listings = {
      "": [entry("z.conf", "file"), entry("data", "dir"), entry("cache", "dir"), entry("sock", "other"), entry("link", "symlink")],
      data: [entry("db.sqlite", "file")],
      cache: [entry("x", "file")],
    };
    const rows = hostTree(listings, new Set(["data"]));
    expect(rows.map((r) => `${r.kind}:${r.path}:${r.depth}`)).toEqual(["folder:cache:0", "folder:data:0", "file:data/db.sqlite:1", "file:link:0", "file:z.conf:0"]);
    // An open folder not read yet shows nothing under it.
    expect(hostTree({ "": [entry("data", "dir")] }, new Set(["data"])).length).toBe(1);
  });

  it("checks a new file's name", () => {
    const rows = hostTree({ "": [entry("a.conf", "file"), entry("data", "dir")] }, new Set());
    expect(hostPathProblem("", rows)).toBe("Give the file a name.");
    expect(hostPathProblem("/etc/x", rows)).toBe("Leave out the leading /.");
    expect(hostPathProblem("../x", rows)).toBe("No empty parts, ./ or ../.");
    expect(hostPathProblem("a.conf", rows)).toBe("a.conf is already there.");
    expect(hostPathProblem("data/new.conf", rows)).toBe("");
  });
});

describe("the ledger", () => {
  const sub = (path: string, used_bytes: number) => ({ path, used_bytes }) as Schema<"Subvolume">;

  it("colours a folder like its segment, a small one like other, an unmeasured one not at all", () => {
    const folders = [sub("/pool/a", 4 * TB), sub("/pool/b", 2 * TB), sub("/pool/c", 1 * TB), sub("/pool/d", TB / 2), sub("/pool/e", -1)];
    const segs = segments(folders, 12 * TB, "/pool", 8 * TB);
    expect(swatchOf(segs, "a", true)).toBe(segmentColor(0));
    expect(swatchOf(segs, "c", true)).toBe(segmentColor(2));
    expect(swatchOf(segs, "d", true)).toBe("var(--muted)");
    expect(swatchOf(segs, "e", false)).toBeNull();
  });

  it("groups folders under their mounted pool", () => {
    const tank = pool({ uuid: "t", mountdir: "/var/mnt/tank" });
    const fast = pool({ uuid: "f", mountdir: "/var/mnt/fast" });
    const away = pool({ uuid: "x", state: "unmounted", mountdir: "" });
    const groups = folderGroups([tank, away, fast], [sub("/var/mnt/fast/db", 1), sub("/var/mnt/tank/media", 2), sub("/var/mnt/tank/photos", 3)]);
    expect(groups.map((g) => [g.pool.uuid, g.folders.map((f) => f.path)])).toEqual([
      ["t", ["/var/mnt/tank/media", "/var/mnt/tank/photos"]],
      ["f", ["/var/mnt/fast/db"]],
    ]);
  });

  it("holds a pool's changes while a disk leaves it", () => {
    expect(poolBlocked(pool())).toBe("");
    const leaving = pool({ operation: { kind: "remove", disk: "/dev/sdc", phase: "moving", left_bytes: 1, started_at: "" } as Pool["operation"] });
    expect(poolBlocked(leaving)).toBe("sdc is leaving the pool; disk changes wait until it is out.");
  });
});

describe("poolDiskState, flaggedDisks, and poolsSummary", () => {
  it("flags failed, read-only, failing, and bad-sector pool disks", () => {
    const ok = { ...disk("a", {}), state: "rw" };
    const worn = { ...disk("b", { bad_sectors: 5 }), state: "rw" };
    const failing = { ...disk("c", { passed: false }), state: "rw" };
    const ro = { ...disk("d", {}), state: "ro" };
    const failed = { ...disk("e", {}), state: "failed" };

    expect(poolDiskState(ok)).toEqual({ tone: "ok", word: "healthy" });
    expect(poolDiskState(ro)).toEqual({ tone: "warn", word: "read-only" });
    expect(poolDiskState(failed)).toEqual({ tone: "bad", word: "failed" });
    expect(flaggedDisks([ok, worn, failing, ro, failed]).map((d) => d.bcachefs_label)).toEqual(["b", "c", "d", "e"]);
  });

  it("summarizes empty, single, and multiple sized pools", () => {
    expect(poolsSummary([])).toBeNull();
    expect(poolsSummary([pool({ usage: undefined })])).toBeNull();

    const p1 = pool({ name: "tank", mountdir: "/var/mnt/tank", data_replicas: 2, usage: { total_bytes: 12 * TB, used_bytes: 3 * TB, available_bytes: 8 * TB, used_percent: 25 } });
    const single = poolsSummary([p1])!;
    expect(single.singlePool?.name).toBe("tank");
    expect(single.count).toBe(1);
    expect(single.used).toBe(1.5 * TB);
    expect(single.free).toBe(4 * TB);
    expect(single.percent).toBe(25);

    const p2 = pool({ name: "apps", mountdir: "/var/mnt/apps", data_replicas: 1, usage: { total_bytes: 4 * TB, used_bytes: 3 * TB, available_bytes: 1 * TB, used_percent: 75 } });
    const multi = poolsSummary([p1, p2])!;
    expect(multi.singlePool).toBeNull();
    expect(multi.count).toBe(2);
    expect(multi.total).toBe(10 * TB);
    expect(multi.used).toBe(4.5 * TB);
    expect(multi.free).toBe(5 * TB);
    expect(multi.percent).toBe(45);
    expect(multi.maxPercent).toBe(75);
  });
});
