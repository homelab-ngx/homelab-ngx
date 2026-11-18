import { describe, expect, it } from "vitest";
import { applyProposal, changedFiles, proposedChanges } from "./drafts.ts";

const buffer = { manifest: "{}\n", flake: "{ }\n" };

describe("a proposal", () => {
  it("lists only the files it changes", () => {
    expect(proposedChanges(buffer, { manifest: "{}\n", flake: "{ x }\n" })).toEqual(["flake"]);
    expect(proposedChanges(buffer, {})).toEqual([]);
  });

  it("applies one file or all of them, leaving the rest", () => {
    const p = { manifest: '{"version": 8}\n', flake: "{ x }\n" };
    expect(applyProposal(buffer, p, "flake")).toEqual({ manifest: "{}\n", flake: "{ x }\n" });
    expect(applyProposal(buffer, p)).toEqual(p);
    expect(buffer).toEqual({ manifest: "{}\n", flake: "{ }\n" });
  });

  it("marks applied files as changed against the saved ones", () => {
    expect(changedFiles(buffer, applyProposal(buffer, { flake: "{ x }\n" }))).toEqual(["flake"]);
  });
});
