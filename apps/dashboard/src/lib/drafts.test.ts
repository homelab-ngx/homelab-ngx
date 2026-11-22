import { describe, expect, it } from "vitest";
import type { DraftFile } from "./api/client.ts";
import { applyProposal, bufferOf, changesOf, fileTree, pathProblem, proposedChanges } from "./drafts.ts";

const file = (path: string, content: string): DraftFile => ({ path, content, size: content.length, binary: false });
const saved: DraftFile[] = [
  file("flake.nix", "{ }\n"),
  file("homelab-stack.json", "{}\n"),
  file("modules/web.nix", "{ web }\n"),
  { path: "logo.png", content: "", size: 9, binary: true },
];

describe("the changes to save", () => {
  it("are none for the saved files", () => {
    expect(changesOf(saved, bufferOf(saved))).toEqual([]);
  });

  it("write edited and new files, delete removed ones first, and leave a binary file alone", () => {
    const b = bufferOf(saved);
    b["flake.nix"] = "{ x }\n";
    b["modules/db.nix"] = "{ db }\n";
    delete b["modules/web.nix"];
    expect(changesOf(saved, b)).toEqual([
      { path: "modules/web.nix", delete: true },
      { path: "flake.nix", content: "{ x }\n" },
      { path: "modules/db.nix", content: "{ db }\n" },
    ]);
  });

  it("turn a rename into a delete and a write", () => {
    const b = bufferOf(saved);
    b["web.nix"] = b["modules/web.nix"] ?? null;
    delete b["modules/web.nix"];
    expect(changesOf(saved, b)).toEqual([
      { path: "modules/web.nix", delete: true },
      { path: "web.nix", content: "{ web }\n" },
    ]);
  });
});

describe("a new file's path", () => {
  const b = bufferOf(saved);
  it("is refused where the daemon refuses it", () => {
    expect(pathProblem("", b)).not.toBe("");
    expect(pathProblem("/etc/x", b)).not.toBe("");
    expect(pathProblem("a//b", b)).not.toBe("");
    expect(pathProblem("../x", b)).not.toBe("");
    expect(pathProblem(".git/config", b)).not.toBe("");
    expect(pathProblem("flake.nix", b)).toBe("flake.nix is already there.");
    expect(pathProblem("logo.png/x", b)).toBe("logo.png is a file.");
    expect(pathProblem("modules", b)).toBe("modules is a folder.");
  });
  it("is taken otherwise", () => {
    expect(pathProblem("modules/db/default.nix", b)).toBe("");
  });
});

describe("the file list", () => {
  it("puts folders first at each level", () => {
    expect(fileTree(["flake.nix", "b/z.nix", "a.txt", "b/c/d.nix"]).map((r) => `${r.depth}${r.kind[0]} ${r.path}`)).toEqual([
      "0f b",
      "1f b/c",
      "2f b/c/d.nix",
      "1f b/z.nix",
      "0f a.txt",
      "0f flake.nix",
    ]);
  });
});

describe("a proposal", () => {
  const buffer = { "homelab-stack.json": "{}\n", "flake.nix": "{ }\n" };

  it("lists only the files it changes", () => {
    expect(proposedChanges(buffer, { "homelab-stack.json": "{}\n", "flake.nix": "{ x }\n", "a.nix": "{ }\n" })).toEqual(["flake.nix", "a.nix"]);
    expect(proposedChanges(buffer, {})).toEqual([]);
  });

  it("applies one file or all of them, leaving the rest", () => {
    const p = { "homelab-stack.json": '{"version": 8}\n', "flake.nix": "{ x }\n" };
    expect(applyProposal(buffer, p, "flake.nix")).toEqual({ "homelab-stack.json": "{}\n", "flake.nix": "{ x }\n" });
    expect(applyProposal(buffer, p)).toEqual(p);
    expect(buffer).toEqual({ "homelab-stack.json": "{}\n", "flake.nix": "{ }\n" });
  });
});
