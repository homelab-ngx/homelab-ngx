import { describe, expect, it } from "vitest";
import openapi from "../../../core/api/openapi.json" with { type: "json" };
import type { Schema } from "#lib/api/client.ts";
import { demoFetchFor } from "./fetch.ts";
import { newHost } from "./host.ts";

/** A demo host on a clock the test moves. */
function demo() {
  let t = Date.UTC(2026, 10, 19, 12);
  const fetch = demoFetchFor(newHost(() => t));
  const send = async <T>(method: string, path: string, body?: unknown) => {
    const r = await fetch(new Request(`http://demo/api${path}`, { method, body: body === undefined ? undefined : JSON.stringify(body) }));
    return { status: r.status, body: (r.status === 204 || r.headers.get("Content-Type") === null ? undefined : await r.json()) as T };
  };
  return {
    get: async <T>(path: string) => (await send<T>("GET", path)).body,
    post: <T>(path: string, body?: unknown) => send<T>("POST", path, body),
    send,
    wait: (ms: number) => (t += ms),
  };
}

const fill: Record<string, string> = { name: "photos", id: "1", run: "100", unit: "x", uuid: "x" };

describe("the demo's API", () => {
  it("answers every GET the daemon's document describes", async () => {
    const d = demo();
    // Paths the demo answers 404 on purpose, the event stream (metrics.ts),
    // and icons: an <img> never goes through the demo's fetch, so the page
    // draws its letter tiles.
    const none = new Set(["/drafts/{name}", "/security/audits/{id}", "/metrics/stream", "/stacks/{name}/icon"]);
    for (const [path, ops] of Object.entries(openapi.paths)) {
      if (!("get" in ops) || none.has(path)) continue;
      const url = path.replace(/{(\w+)}/g, (_, n: string) => fill[n] ?? "1").replace("/stack-updates/1", "/stack-updates/1000");
      const r = await d.send("GET", url + (path === "/metrics/history" ? "?range=24h" : path === "/logs" ? "?unit=x" : ""));
      expect(r.status, path).toBe(200);
    }
  });

  it("answers a change it does not act out with not_in_demo", async () => {
    const d = demo();
    for (const [method, path] of [["DELETE", "/stacks/photos"], ["PUT", "/host-settings/host"], ["POST", "/stacks"]] as const) {
      const r = await d.send<{ code: string }>(method, path, {});
      expect([r.status, r.body.code], path).toEqual([403, "not_in_demo"]);
    }
    expect((await d.get<Schema<"Stack">[]>("/stacks")).length).toBe(6);
  });

  it("makes a pool from the spare disks, removes a disk from it and deletes it", async () => {
    const d = demo();
    type Status = Schema<"StorageStatus">;
    const pool = async (name: string) => (await d.get<Status>("/storage")).pools.find((p) => p.name === name);
    expect((await d.get<Status>("/storage")).unassigned.map((x) => [x.name, x.kind, x.contents])).toEqual([["sdc", "hdd", ""], ["sdd", "ssd", "2 partitions"]]);

    const body = { name: "fast", disks: ["/dev/sdc", "/dev/sdd"], copies: 1 };
    expect([(await d.post<{ code: string }>("/storage/pools", body)).body.code]).toEqual(["confirm"]);
    expect((await d.post("/storage/pools", { ...body, confirm: "fast" })).status).toBe(201);
    const made = (await pool("fast"))!;
    expect([made.mountdir, made.background_target, made.disks.map((x) => x.bcachefs_label)]).toEqual(["/var/mnt/fast", "hdd", ["hdd.hdd1", "ssd.ssd1"]]);
    expect((await d.get<Status>("/storage")).unassigned).toEqual([]);

    // The only SSD of a pool that uses both stays.
    expect((await d.send<{ code: string }>("DELETE", `/storage/pools/${made.uuid}/disks?disk=/dev/sdd`)).body.code).toBe("last_of_role");
    const tank = (await pool("tank"))!;

    expect((await d.send<{ code: string }>("DELETE", `/storage/pools/${tank.uuid}?confirm=tank`)).body.code).toBe("pool_in_use");
    expect((await d.send<{ code: string }>("DELETE", `/storage/pools/${made.uuid}?confirm=tank`)).body.code).toBe("confirm");
    expect((await d.send("DELETE", `/storage/pools/${made.uuid}?confirm=fast`)).status).toBe(204);
    expect([(await pool("fast")) === undefined, (await d.get<Status>("/storage")).unassigned.map((x) => x.contents)]).toEqual([true, ["", ""]]);
  });

  it("adds a disk to a pool and follows its removal", async () => {
    const d = demo();
    type Status = Schema<"StorageStatus">;
    const tank = async () => (await d.get<Status>("/storage")).pools[0]!;
    const uuid = (await tank()).uuid;
    expect((await d.post(`/storage/pools/${uuid}/disks`, { disk: "/dev/sdc" })).status).toBe(204);
    expect((await tank()).disks.map((x) => x.bcachefs_label)).toEqual(["hdd.a", "hdd.b", "ssd.a", "hdd.hdd3"]);

    expect((await d.send("DELETE", `/storage/pools/${uuid}/disks?disk=/dev/sdb`)).status).toBe(202);
    expect((await d.post<{ code: string }>(`/storage/pools/${uuid}/disks`, { disk: "/dev/sdd", confirm: "tank" })).body.code).toBe("busy");
    d.wait(6000);
    const mid = (await tank()).operation!;
    expect([mid.phase, mid.left_bytes < mid.total_bytes, mid.left_bytes > 0]).toEqual(["moving", true, true]);
    d.wait(7000);
    const after = await tank();
    expect([after.operation?.phase, after.disks.map((x) => x.name)]).toEqual(["done", ["sda", "nvme1n1", "sdc"]]);
    expect((await d.get<Status>("/storage")).unassigned.map((x) => x.name)).toEqual(["sdd", "sdb"]);
    expect((await d.send("DELETE", `/storage/pools/${uuid}/operation`)).status).toBe(204);
    expect((await tank()).operation).toBeUndefined();
  });

  it("approves a stack's new grant, then updates it", async () => {
    const d = demo();
    const before = await d.get<Schema<"Stack">>("/stacks/home");
    expect([before.update_state, before.new_grants.length]).toEqual(["needs_approval", 1]);
    await d.post("/stacks/home/approve", { rev: before.fetched.rev });
    expect((await d.get<Schema<"Stack">>("/stacks/home")).update_state).toBe("commits_available");

    expect((await d.post("/stacks/home/update")).status).toBe(202);
    expect((await d.get<Schema<"Stack">>("/stacks/home")).operation?.kind).toBe("update");
    d.wait(7000);
    const after = await d.get<Schema<"Stack">>("/stacks/home");
    expect([after.operation, after.update_state, after.generation]).toEqual([undefined, "up_to_date", before.generation + 1]);
    const log = await d.get<Schema<"StackUpdate">[]>("/stack-updates?stack=home");
    expect([log[0]!.kind, log[0]!.result]).toEqual(["update", "healthy"]);
  });

  it("refuses to approve a commit that is not the fetched one", async () => {
    const r = await demo().post<{ code: string }>("/stacks/home/approve", { rev: "0000" });
    expect([r.status, r.body.code]).toEqual([409, "stale_revision"]);
  });

  it("updates a stack and rolls it back", async () => {
    const d = demo();
    const before = await d.get<Schema<"Stack">>("/stacks/media");
    await d.post("/stacks/media/update");
    d.wait(7000);
    expect((await d.get<Schema<"Stack">>("/stacks/media")).generation).toBe(before.generation + 1);
    await d.post("/stacks/media/rollback", { generation: before.generation });
    d.wait(4000);
    const after = await d.get<Schema<"Stack">>("/stacks/media");
    expect([after.generation, after.operation]).toEqual([before.generation, undefined]);
  });

  it("runs a backup and keeps the run", async () => {
    const d = demo();
    const { body } = await d.post<{ run_id: number }>("/backups/2/run");
    expect((await d.get<Schema<"Backup">>("/backups/2")).running).toBe(true);
    expect((await d.post("/backups/2/run")).status).toBe(409);
    d.wait(9000);
    const b = await d.get<Schema<"Backup">>("/backups/2");
    expect([b.running, b.recent_runs[0]!.id, b.recent_runs[0]!.result]).toEqual([false, body.run_id, "success"]);
    expect((await d.get<Schema<"BackupRunDetail">>(`/backup-runs/${body.run_id}`)).items.every((i) => i.result === "success")).toBe(true);
  });

  it("downloads the new image, installs it and boots it", async () => {
    const d = demo();
    await d.post("/system-updates/image/download");
    d.wait(7000);
    expect((await d.get<Schema<"ImageStatus">>("/system-updates/image")).available).toEqual(["0.2.6"]);
    await d.post("/system-updates/image/release", { reboot: "" });
    d.wait(6000);
    const staged = await d.get<Schema<"ImageStatus">>("/system-updates/image");
    expect([staged.pending_reboot, staged.installed[0]]).toEqual([true, { version: "0.2.6", state: "pending", tries_left: 3 }]);
    const first = (await d.get<Schema<"PowerStatus">>("/system/power")).boot_id;
    expect((await d.post<Schema<"PowerStatus">>("/system/power", { action: "reboot" })).status).toBe(202);
    const going = await d.get<Schema<"PowerStatus">>("/system/power");
    expect([going.phase, going.steps.every((s) => s.kind === "stack")]).toEqual(["stopping_stacks", true]);
    d.wait(3000);
    // The last stack is slow to stop: the page is told what it waits for.
    expect((await d.get<Schema<"PowerStatus">>("/system/power")).steps.some((s) => s.waiting_on)).toBe(true);
    d.wait(4000);
    expect((await d.get<Schema<"PowerStatus">>("/system/power")).steps.some((s) => s.kind === "mount")).toBe(true);
    d.wait(3000);
    // Down: the host answers nothing for a while.
    await expect(d.get("/system/power")).rejects.toThrow(TypeError);
    d.wait(30_000);
    const back = await d.get<Schema<"PowerStatus">>("/system/power");
    expect([back.phase, back.boot_id === first]).toEqual(["idle", false]);
    const after = await d.get<Schema<"ImageStatus">>("/system-updates/image");
    expect([after.booted, after.pending_reboot]).toEqual(["0.2.6", false]);
  });

  it("installs and reboots in one run, the core left for the new boot", async () => {
    const d = demo();
    await d.post("/system-updates/image/download");
    d.wait(7000);
    const first = (await d.get<Schema<"PowerStatus">>("/system/power")).boot_id;
    await d.post("/system-updates/image/release", { reboot: "now" });
    d.wait(1000);
    // No restart before the reboot: the daemon answers throughout the install.
    expect((await d.get<Schema<"PowerStatus">>("/system/power")).phase).toBe("idle");
    expect((await d.get<Schema<"CoreDeploy">[]>("/system-updates/deploys"))[0]!.result).toBe("running");
    d.wait(600);
    const done = (await d.get<Schema<"CoreDeploy">[]>("/system-updates/deploys"))[0]!;
    expect([done.result, done.phase]).toEqual(["healthy", "Installed 0.2.6; rebooting"]);
    d.wait(500);
    expect((await d.get<Schema<"PowerStatus">>("/system/power")).phase).toBe("stopping_stacks");
    d.wait(10_000);
    await expect(d.get("/system/power")).rejects.toThrow(TypeError);
    d.wait(30_000);
    expect((await d.get<Schema<"PowerStatus">>("/system/power")).boot_id).not.toBe(first);
    expect((await d.get<Schema<"ImageStatus">>("/system-updates/image")).booted).toBe("0.2.6");
  });

  it("asks to thaw a frozen stack before a reboot", async () => {
    const d = demo();
    const [first] = await d.get<Schema<"Stack">[]>("/stacks");
    await d.post(`/stacks/${first!.name}/freeze`);
    expect((await d.get<Schema<"PowerStatus">>("/system/power")).frozen).toEqual([first!.name]);
    expect((await d.post("/system/power", { action: "reboot" })).status).toBe(409);
    expect((await d.post("/system/power", { action: "reboot", thaw: true })).status).toBe(202);
    expect((await d.get<Schema<"Stack">>(`/stacks/${first!.name}`)).frozen).toBe(false);
  });

  it("stops and starts a stack and its units", async () => {
    const d = demo();
    await d.post("/stacks/git/stop");
    expect((await d.get<Schema<"ServiceInfo">[]>("/services")).find((s) => s.stack === "git")!.active_state).toBe("inactive");
    await d.post("/stacks/git/start");
    expect((await d.get<Schema<"Stack">>("/stacks/git")).running).toBe(true);
  });

  it("restarts a stack as a phased daemon operation", async () => {
    const d = demo();
    const { body } = await d.post<Schema<"Stack">>("/stacks/git/restart");
    expect(body.operation?.kind).toBe("restart");
    expect(body.operation?.finished_at).toBeUndefined();
    d.wait(1500);
    const done = await d.get<Schema<"Stack">>("/stacks/git");
    expect(done.running).toBe(true);
    expect(done.operation).toBeUndefined();
  });
});
