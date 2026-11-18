import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
import { dayMarks, type Run } from "../../backups.ts";
import RunStrip from "./RunStrip.svelte";

const run = (id: number, started: string, result: Run["result"], bytes = 0): Run => ({
  id,
  backup_id: 1,
  backup_name: "Photos",
  trigger: "cron",
  started_at: started,
  finished_at: result === "running" ? null : new Date(new Date(started).getTime() + 360_000).toISOString(),
  result,
  error: "",
  files: 0,
  bytes,
});

afterEach(cleanup);

describe("RunStrip", () => {
  const now = new Date(2026, 10, 16, 12);
  const at = (day: number) => new Date(2026, 10, day, 4).toISOString();
  const days = dayMarks([run(7, at(12), "success", 212_000_000), run(8, at(15), "failure"), run(9, at(16), "partial")], now);

  it("draws one mark a day and names each in words", () => {
    render(RunStrip, { props: { days, href: (r) => `/backups/1?run=${r}`, name: "Photos" } });
    expect(screen.getByRole("list", { name: "Photos: the last 14 days" }).children).toHaveLength(14);
    expect(screen.getByRole("link", { name: "12 Nov: OK, 6 min, 212 MB" }).getAttribute("href")).toBe("/backups/1?run=7");
    expect(screen.getByRole("link", { name: /^15 Nov: Failed/ }).getAttribute("href")).toBe("/backups/1?run=8");
    expect(screen.getByRole("link", { name: /^16 Nov: Partly failed/ })).toBeTruthy();
    expect(screen.getByText("14 Nov: did not run")).toBeTruthy();
  });

  it("links only the days that have a run, and marks the open one", () => {
    render(RunStrip, { props: { days, href: (r) => `?run=${r}`, selected: 8, name: "Photos" } });
    const links = screen.getAllByRole("link");
    expect(links).toHaveLength(3);
    expect(links.filter((l) => l.getAttribute("aria-current") === "true").map((l) => l.getAttribute("href"))).toEqual(["?run=8"]);
  });

  it("says how a day went in the shape of its mark, not only its colour", () => {
    render(RunStrip, { props: { days, href: () => "#", name: "Photos" } });
    const fill = (name: RegExp) => screen.getByRole("link", { name }).firstElementChild!.className;
    expect(fill(/^12 Nov/)).toContain("h-full");
    expect(fill(/^16 Nov/)).toContain("h-2/3");
    expect(fill(/^15 Nov/)).toContain("h-1/4");
  });
});
