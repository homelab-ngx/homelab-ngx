import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
import ChangelogCard from "./ChangelogCard.svelte";
import type { ReleaseNote } from "./changelog.ts";
import type { ImageStatus } from "./core.ts";

const img = (over: Partial<ImageStatus> = {}): ImageStatus => ({
  booted: "0.4.5",
  installed: [
    { version: "0.4.5", state: "booted" },
    { version: "0.4.4", state: "good" },
  ],
  available: [],
  pending_reboot: false,
  core: { active: "0.4.5", installed: ["0.4.5"], available: [] },
  source: "",
  release: { url: "", latest: "0.4.5", checked_at: null },
  ...over,
});

const releases: ReleaseNote[] = [
  {
    version: "0.4.6",
    date: "2026-11-26",
    changes: [{ area: "host", text: "add outbound SMTP relay" }],
    url: "https://github.com/homelab-ngx/homelab-ngx/releases/tag/v0.4.6",
  },
  {
    version: "0.4.5",
    date: "2026-11-26",
    changes: [{ area: "backups", text: "preview backup changes before copying" }],
  },
  {
    version: "0.4.4",
    date: "2026-11-25",
    changes: [{ area: "stacks", text: "add daemon restart" }],
  },
];

afterEach(() => cleanup());

describe("ChangelogCard", () => {
  it("shows empty copy when no changelog is loaded", () => {
    render(ChangelogCard, { props: { image: img(), releases: [], loading: false } });
    expect(screen.getByText("No release notes yet.")).toBeTruthy();
  });

  it("shows ready update changelog and folds earlier releases", () => {
    render(ChangelogCard, {
      props: {
        image: img({ available: ["0.4.6"], release: { url: "", latest: "0.4.6", checked_at: null } }),
        releases,
      },
    });
    expect(screen.getByText("add outbound SMTP relay")).toBeTruthy();
    expect(screen.getByText("ready")).toBeTruthy();
    expect(screen.getByText("Earlier releases")).toBeTruthy();
    expect(screen.getByText("2 versions")).toBeTruthy();
  });

  it("shows running release first when up to date", () => {
    render(ChangelogCard, {
      props: {
        image: img(),
        releases: releases.slice(1),
      },
    });
    expect(screen.getByText("preview backup changes before copying")).toBeTruthy();
    expect(screen.getByText("running")).toBeTruthy();
    expect(screen.getByText("1 version")).toBeTruthy();
  });
});
