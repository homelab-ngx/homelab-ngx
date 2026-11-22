import { describe, expect, it } from "vitest";
import type { ServiceInfo, Stack } from "./api/client.ts";
import { describeSource, draftOf, hostUnitName, stackIcon, unitIcon, unitWord, folderSource, gitSource, grants, health, nixpkgsLabel, nixpkgsOnly, removedGrants, runningNixpkgs, sourceHref, stackTitle, stackTitles, stepOf, validStackName } from "./stacks.ts";

const unit = (name: string, active_state: string): ServiceInfo =>
  ({ name, stack: "media", active_state, sub_state: "", type: "Native", description: "", unit_name: name, started_at: null }) as ServiceInfo;

const stack = (over: Partial<Stack> = {}): Stack =>
  ({
    name: "media",
    source: "github:example/stack-media",
    running: true,
    want_running: true,
    generation: 3,
    generations: [],
    fetched: { rev: "b", url: "", at: "", manifest: { version: 1, name: "media", routes: [], storage: [], egress: [], gpu: false, health: { units: [] } } },
    ...over,
  }) as Stack;

describe("gitSource", () => {
  it("turns GitHub URLs into github: sources", () => {
    expect(gitSource("https://github.com/example/stack-email", "")).toBe("github:example/stack-email");
    expect(gitSource("https://github.com/example/stack-email.git/", "dev")).toBe("github:example/stack-email/dev");
    expect(gitSource("git@github.com:example/stack-email.git", "")).toBe("github:example/stack-email");
  });
  it("keeps other hosts as git+ sources with the branch as ref", () => {
    expect(gitSource("https://git.example/a/b.git", "main")).toBe("git+https://git.example/a/b.git?ref=main");
    expect(gitSource("git@git.example:a/b.git", "")).toBe("git+ssh://git@git.example/a/b.git");
  });
  it("refuses what is not a git URL", () => {
    expect(gitSource("homelab-email", "")).toBeNull();
    expect(gitSource("http://insecure/a/b", "")).toBeNull();
  });
});

describe("folderSource and describeSource", () => {
  it("round-trips a local checkout", () => {
    const s = folderSource("/home/alice/repos/homelab-media/")!;
    expect(s).toBe("git+file:///home/alice/repos/homelab-media");
    expect(describeSource(s)).toEqual({ repo: "/home/alice/repos/homelab-media", branch: "checked-out branch", local: true });
    expect(folderSource("repos/x")).toBeNull();
  });
  it("names the repo and branch of a github source", () => {
    expect(describeSource("github:example/stack-media/dev")).toEqual({ repo: "github.com/example/stack-media", branch: "dev", local: false });
  });
});

describe("health", () => {
  it("marks a stack with a failed service degraded and lists it (AE1)", () => {
    const svcs = [unit("media-sonarr", "failed"), ...Array.from({ length: 8 }, (_, i) => unit(`media-s${i}`, "active"))];
    const h = health(stack(), svcs);
    expect(h.state).toBe("degraded");
    expect([h.active, h.total]).toEqual([8, 9]);
    expect(h.failed.map((u) => u.name)).toEqual(["media-sonarr"]);
  });
  it("leaves a disabled service out", () => {
    const h = health(stack(), [unit("media-plex", "active"), { ...unit("media-sonarr", "inactive"), masked: true }]);
    expect(h.state).toBe("running");
    expect([h.active, h.total]).toEqual([1, 1]);
  });
  it("tells stopped, starting and not installed apart", () => {
    expect(health(stack({ running: false, want_running: false }), []).state).toBe("stopped");
    expect(health(stack({ running: false }), []).state).toBe("starting");
    expect(health(stack({ generation: 0 }), []).state).toBe("not-installed");
    expect(health(stack({ frozen: true }), []).state).toBe("frozen");
  });
});

describe("grants", () => {
  it("words address variables and the LAN like the daemon", () => {
    const m = { version: 8, name: "nvr", routes: [], egress: [{ to: "$CAMERAS", port: 554, proto: "tcp" }], ports: [{ port: 8555, proto: "udp", from: ["lan", "$HA"], to: 8555 }], gpu: false, containers: false, health: { units: [] } };
    expect(grants(m as never).map((g) => g.detail)).toEqual([
      "CAMERAS port 554/tcp",
      "host port 8555/udp from LAN, HA to stack port 8555",
    ]);
  });
  it("words every grant and finds the ones an update drops", () => {
    const m = { version: 2, name: "media", routes: [{ host: "music.example.com", port: 4533 }], storage: [{ host_path: "/pool/music", mount: "/music", read_only: false }], egress: [{ to: "192.168.1.102/32", port: 1883, proto: "tcp" }], gpu: true, containers: true, health: { units: [] } };
    expect(grants(m as never).map((g) => g.kind)).toEqual(["route", "storage", "gpu", "egress", "containers"]);
    const s = stack({ approved: { rev: "a", url: "", at: "", manifest: m as never } });
    expect(removedGrants(s).map((g) => g.kind)).toEqual(["route", "storage", "gpu", "egress", "containers"]);
  });
  it("words path variables like the daemon and does not drop storage that became one", () => {
    const v4 = { version: 4, name: "media", routes: [], storage: [{ host_path: "/pool/media", mount: "/media", read_only: false }], egress: [], gpu: false, containers: false, health: { units: [] } };
    const v5 = {
      ...v4,
      version: 5,
      storage: [],
      variables: [
        { name: "MEDIA_DIR", kind: "path", description: "", required: true, default: "/pool/media", mount: "/media", read_only: false },
        { name: "IN", kind: "path", description: "", required: false, mount: "/in", read_only: true },
        { name: "TZ", kind: "env", description: "", required: false },
      ],
    };
    expect(grants(v5 as never).map((g) => g.detail)).toEqual([
      "MEDIA_DIR: /pool/media (default) at /media, read-write",
      "IN at /in, read-only",
    ]);
    const s = stack({ approved: { rev: "a", url: "", at: "", manifest: v4 as never }, fetched: { rev: "b", url: "", at: "", manifest: v5 as never } });
    expect(removedGrants(s)).toEqual([]);
  });
});

describe("stepOf", () => {
  it("follows the daemon's phases", () => {
    expect(stepOf("building abcdef012345")).toBe(1);
    expect(stepOf("switching to generation 4")).toBe(2);
    expect(stepOf("waiting for generation 4 to be healthy")).toBe(3);
    expect(stepOf("running generation 4 (abc)")).toBe(4);
  });
});

describe("stack titles", () => {
  it("shows the label, else the name", () => {
    expect(stackTitle(stack())).toBe("media");
    expect(stackTitle(stack({ label: "Movies & TV" }))).toBe("Movies & TV");
  });

  it("looks a title up by name, and keeps a name it does not know", () => {
    const title = stackTitles([stack({ label: "Movies & TV" }), stack({ name: "email", label: "" })]);
    expect(title("media")).toBe("Movies & TV");
    expect(title("email")).toBe("email");
    expect(title("gone")).toBe("gone");
    expect(stackTitles(undefined)("media")).toBe("media");
  });

  it("checks a name the way the daemon does", () => {
    expect(validStackName("photos")).toBe(true);
    expect(validStackName("tv-2")).toBe(true);
    for (const bad of ["", "Photos", "2tv", "a-very-long-name", "pho tos"]) expect(validStackName(bad)).toBe(false);
  });
});

describe("nixpkgs", () => {
  it("words a channel or commit", () => {
    expect(nixpkgsLabel("nixos-unstable")).toBe("Unstable");
    expect(nixpkgsLabel("nixos-26.11")).toBe("26.11");
    expect(nixpkgsLabel("d5cc6833999746cffdfe0598f7bc801a57b887a9")).toBe("Commit d5cc683");
  });

  it("finds the running generation's commit", () => {
    const s = stack({ generation: 2, generations: [{ id: 2, rev: "a", built_at: "", failed: false, nixpkgs_rev: "x" }] } as Partial<Stack>);
    expect(runningNixpkgs(s)).toBe("x");
    expect(runningNixpkgs(stack({ generation: 0 }))).toBeUndefined();
  });
});

describe("nixpkgsOnly", () => {
  const gen = { id: 2, rev: "a", built_at: "", failed: false, nixpkgs_rev: "x" };
  const approved = { rev: "a", url: "", at: "", manifest: {} } as Stack["approved"];
  it("is true when only nixpkgs moved", () => {
    expect(nixpkgsOnly(stack({ generation: 2, generations: [gen], approved, nixpkgs_rev: "y" } as Partial<Stack>))).toBe(true);
    expect(nixpkgsOnly(stack({ generation: 2, generations: [gen], approved, nixpkgs_rev: "x" } as Partial<Stack>))).toBe(false);
    expect(nixpkgsOnly(stack({ generation: 2, generations: [{ ...gen, rev: "b" }], approved, nixpkgs_rev: "y" } as Partial<Stack>))).toBe(false);
  });
});

describe("draftOf", () => {
  it("names the draft of a source the daemon keeps", () => {
    expect(draftOf("git+file:///var/lib/homelab-daemon/drafts/site")).toBe("site");
    expect(draftOf("git+file:///var/lib/homelab-daemon/drafts/site?ref=HEAD")).toBe("site");
    expect(draftOf("git+file:///home/me/repos/site")).toBeNull();
    expect(draftOf("github:o/drafts")).toBeNull();
  });
});

describe("sourceHref", () => {
  it("links GitHub's repo and branch pages, an https remote as is, and nothing else", () => {
    expect(sourceHref("github:example/stack-media/dev")).toEqual({ repo: "https://github.com/example/stack-media", branch: "https://github.com/example/stack-media/tree/dev" });
    expect(sourceHref("github:example/stack-media")).toEqual({ repo: "https://github.com/example/stack-media", branch: null });
    expect(sourceHref("git+https://git.example.com/me/stack.git?ref=main")).toEqual({ repo: "https://git.example.com/me/stack.git", branch: null });
    expect(sourceHref("https://git.example.com/me/stack")).toEqual({ repo: "https://git.example.com/me/stack", branch: null });
    expect(sourceHref("git+ssh://git@git.example.com/me/stack?ref=main")).toEqual({ repo: null, branch: null });
    expect(sourceHref("git+file:///srv/stacks/paperless")).toEqual({ repo: null, branch: null });
  });
});

describe("icons", () => {
  const withIcons = (icon?: string) => {
    const m = { version: 9, name: "players", routes: [], storage: [], egress: [], gpu: false, health: { units: [] }, icon, services: [{ unit: "plex.service", icon: "plex" }, { unit: "players-watch.service", icon: "icons/w.png" }] };
    return stack({ name: "players", fetched: { rev: "b", url: "", at: "", manifest: m }, approved: { rev: "a", url: "", at: "", manifest: m } } as unknown as Partial<Stack>);
  };
  const svc = (unit_name: string) => ({ ...unit(unit_name.replace(/\.service$/, ""), "active"), unit_name }) as ServiceInfo;

  it("names a manifest's unit as the host does", () => {
    expect(hostUnitName("players", "plex.service")).toBe("players-plex.service");
    expect(hostUnitName("players", "players-watch.service")).toBe("players-watch.service");
  });

  it("points at the daemon's icon for a stack and its services, and at nothing without one", () => {
    expect(stackIcon(withIcons("plex"))).toBe("/api/stacks/players/icon?rev=a");
    expect(stackIcon(withIcons())).toBeUndefined();
    expect(unitIcon(withIcons(), svc("players-plex.service"))).toBe("/api/stacks/players/icon?unit=plex.service&rev=a");
    expect(unitIcon(withIcons(), svc("players-watch.service"))).toBe("/api/stacks/players/icon?unit=players-watch.service&rev=a");
    expect(unitIcon(withIcons(), svc("players-jellyfin.service"))).toBeUndefined();
  });

  it("says a disabled service is disabled", () => {
    expect(unitWord({ ...unit("media-plex", "inactive"), masked: true } as ServiceInfo)).toBe("disabled");
    expect(unitWord(unit("media-plex", "failed"))).toBe("failed");
  });
});
