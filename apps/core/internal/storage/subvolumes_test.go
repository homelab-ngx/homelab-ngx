package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"

	"homelab-ngx.com/core/internal/storage/bcachefs"
)

func TestClassifyProfile(t *testing.T) {
	t.Parallel()
	tiered := bcachefs.Pool{
		ForegroundTarget: "ssd",
		BackgroundTarget: "hdd",
		PromoteTarget:    "ssd",
	}
	noPromote := bcachefs.Pool{
		ForegroundTarget: "ssd",
		BackgroundTarget: "hdd",
	}
	untiered := bcachefs.Pool{}

	cases := []struct {
		name string
		pool bcachefs.Pool
		opts bcachefs.FileOptions
		want string
	}{
		{"untiered pool", untiered, bcachefs.FileOptions{ForegroundTarget: "ssd"}, ""},
		{"default cached", tiered, bcachefs.FileOptions{}, ProfileCached},
		{"explicit cached", tiered, bcachefs.FileOptions{ForegroundTarget: "ssd", BackgroundTarget: "hdd", PromoteTarget: "ssd"}, ProfileCached},
		{"cached on pool without promote", noPromote, bcachefs.FileOptions{}, ProfileCached},
		{"hot", tiered, bcachefs.FileOptions{ForegroundTarget: "ssd", BackgroundTarget: "ssd", PromoteTarget: "ssd"}, ProfileHot},
		{"hot without promote", tiered, bcachefs.FileOptions{ForegroundTarget: "ssd", BackgroundTarget: "ssd", PromoteTarget: "none"}, ProfileHot},
		{"cold", tiered, bcachefs.FileOptions{ForegroundTarget: "hdd", BackgroundTarget: "hdd", PromoteTarget: "none"}, ProfileCold},
		{"hot data with hdd promote is custom", tiered, bcachefs.FileOptions{ForegroundTarget: "ssd", BackgroundTarget: "ssd", PromoteTarget: "hdd"}, ProfileCustom},
		{"cold without promote=none inherits ssd promote -> custom", tiered, bcachefs.FileOptions{ForegroundTarget: "hdd", BackgroundTarget: "hdd"}, ProfileCustom},
		{"foreground only override -> custom", tiered, bcachefs.FileOptions{ForegroundTarget: "hdd"}, ProfileCustom},
	}
	for _, tc := range cases {
		if got := classifyProfile(tc.pool, tc.opts); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStatusIncludesDiskUsageCacheReconcileAndSubvolumeProfiles(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.svc.bc.StubStatfs(func(_ string, st *syscall.Statfs_t) error {
		st.Bsize = 4096
		st.Blocks = 1000
		st.Bfree = 750
		st.Bavail = 750
		return nil
	})
	h.fake.On("lsblk").Reply(lsblk(true))
	fsUsageOut, err := os.ReadFile("bcachefs/testdata/fs-usage.txt")
	if err != nil {
		t.Fatal(err)
	}
	// Replace testdata device names with the harness's sda/sdb/sdc rows and keep cache/reconcile headers.
	usageText := strings.NewReplacer("vdc", "sda", "vdb1", "sdb", "vdb2", "sdc").Replace(string(fsUsageOut))
	h.fake.On("bcachefs", "fs", "usage").Reply(usageText)
	h.fake.On("bcachefs", "subvolume", "list").Reply(
		"Path                     ID       Created          Flags\n" +
			"photos                   256      2026-05-27 23:47 -\n" +
			"tmp                      257      2026-05-27 23:47 -\n" +
			"archive                  258      2026-05-27 23:47 -\n")
	h.fake.On("bcachefs", "get-file-option").Reply(
		"/var/mnt/pool/tmp\tdata_replicas\t1\n" +
			"/var/mnt/pool/tmp\tforeground_target\tssd\n" +
			"/var/mnt/pool/tmp\tbackground_target\tssd\n" +
			"/var/mnt/pool/archive\tforeground_target\thdd\n" +
			"/var/mnt/pool/archive\tbackground_target\thdd\n" +
			"/var/mnt/pool/archive\tpromote_target\tnone\n")

	st, err := h.svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := st.Pools[0]
	if p.Usage == nil || p.Usage.CachedBytes != 4294967296 || p.Usage.ReconcileDataBytes != 0 || p.Usage.ReconcileMetadataBytes != 22020096 {
		t.Fatalf("pool usage = %+v", p.Usage)
	}
	diskByName := map[string]Disk{}
	for _, d := range p.Disks {
		diskByName[d.Name] = d
	}
	if u := diskByName["sda"].Usage; u == nil || u.UsedBytes != 4316987392 || u.UsedPercent != 25 || u.SizeBytes != 17042501632 {
		t.Fatalf("sda usage = %+v", u)
	}
	if diskByName["sdd"].Usage != nil {
		t.Fatalf("unmatched disk sdd should have nil usage, got %+v", diskByName["sdd"].Usage)
	}

	subByPath := map[string]Subvolume{}
	for _, sv := range st.Subvolumes {
		subByPath[sv.Path] = sv
	}
	if sv := subByPath["/var/mnt/pool/photos"]; sv.Profile != ProfileCached || sv.DataReplicas != 0 {
		t.Fatalf("photos = %+v", sv)
	}
	if sv := subByPath["/var/mnt/pool/tmp"]; sv.Profile != ProfileHot || sv.DataReplicas != 1 {
		t.Fatalf("tmp = %+v", sv)
	}
	if sv := subByPath["/var/mnt/pool/archive"]; sv.Profile != ProfileCold || sv.PromoteTarget != "none" {
		t.Fatalf("archive = %+v", sv)
	}
}

func TestUpdateSubvolumeHTTP(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true, true)
	do := serveAPI(t, w.harness)
	w.fake.On("bcachefs", "subvolume", "list").Reply(
		"Path   ID   Created          Flags\n" +
			"photos 256  2026-05-27 23:47 -\n" +
			"tmp    257  2026-05-27 23:47 -\n")
	w.fake.On("bcachefs", "set-file-option").Reply("")

	// 1. Hot sets all three targets to ssd; symlink /mnt/pool/tmp canonicalizes to /var/mnt/pool/tmp.
	// Fixture show-super.txt has data_replicas=1, so copies=2 writes an override.
	if resp, m := do("PATCH", "/api/storage/subvolume", `{"path":"/mnt/pool/tmp","profile":"hot","copies":2}`); resp.Code != http.StatusNoContent {
		t.Fatalf("hot+2x: %d %v", resp.Code, m)
	}
	// 2. Cold sets fg/bg to hdd and promote to none.
	if resp, m := do("PATCH", "/api/storage/subvolume", `{"path":"/var/mnt/pool/photos","profile":"cold"}`); resp.Code != http.StatusNoContent {
		t.Fatalf("cold: %d %v", resp.Code, m)
	}
	// 3. Fixture show-super.txt has promote_target=none, so Cached sets explicit targets.
	// Also copies=1 (pool default) clears data_replicas with `-`.
	if resp, m := do("PATCH", "/api/storage/subvolume", `{"path":"/var/mnt/pool/tmp","profile":"cached","copies":1}`); resp.Code != http.StatusNoContent {
		t.Fatalf("cached+1x: %d %v", resp.Code, m)
	}
	// 4. Copies only leaves placement targets untouched.
	if resp, m := do("PATCH", "/api/storage/subvolume", `{"path":"/var/mnt/pool/tmp","copies":0}`); resp.Code != http.StatusNoContent {
		t.Fatalf("copies=0: %d %v", resp.Code, m)
	}

	var got []string
	for _, call := range w.fake.CallsMatching("bcachefs", "set-file-option") {
		got = append(got, strings.Join(call.Args, " "))
	}
	want := []string{
		"set-file-option --data_replicas=2 --foreground_target=ssd --background_target=ssd --promote_target=ssd /var/mnt/pool/tmp",
		"set-file-option --foreground_target=hdd --background_target=hdd --promote_target=none /var/mnt/pool/photos",
		"set-file-option --data_replicas=- --foreground_target=ssd --background_target=hdd --promote_target=ssd /var/mnt/pool/tmp",
		"set-file-option --data_replicas=- /var/mnt/pool/tmp",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("set-file-option calls:\n got %q\nwant %q", got, want)
	}

	// Error cases.
	for _, tc := range []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"neither profile nor copies", `{"path":"/var/mnt/pool/tmp"}`, http.StatusBadRequest, "invalid_profile"},
		{"invalid profile", `{"path":"/var/mnt/pool/tmp","profile":"warm"}`, http.StatusUnprocessableEntity, "bad_request"},
		{"unknown subvolume", `{"path":"/var/mnt/pool/missing","profile":"hot"}`, http.StatusNotFound, "unknown_subvolume"},
		{"path outside any pool", `{"path":"/tmp/other","profile":"hot"}`, http.StatusNotFound, "unknown_subvolume"},
	} {
		resp, m := do("PATCH", "/api/storage/subvolume", tc.body)
		if resp.Code != tc.status || m["code"] != tc.code {
			t.Errorf("%s: got %d %v, want %d %s: %s", tc.name, resp.Code, m["code"], tc.status, tc.code, resp.Body)
		}
	}
}

func TestUpdateSubvolumeErrorsTieredDisksAndTools(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true, true)
	// Untiered 2-disk pool (hdd only, no targets; use sde/sdg so show-super doesn't hit newHarness's /dev/sda rule).
	untieredSuper := "External UUID: " + poolUUID + "\nLabel: pool\n  data_replicas: 1\n  metadata_replicas: 2\n" +
		"Device 0: /dev/sde\n  Label: hdd.hdd1\n  State: rw\n" +
		"Device 1: /dev/sdg\n  Label: hdd.hdd2\n  State: rw\n"
	w2 := newWorld(t, false, false)
	w2.disks = []*tdisk{
		{name: "sde", rota: true, fstype: "bcachefs", label: "pool", uuid: poolUUID, mount: "/var/mnt/pool"},
		{name: "sdg", rota: true, fstype: "bcachefs", label: "pool", uuid: poolUUID},
	}
	w2.fake.On("bcachefs", "show-super").Reply(untieredSuper)
	w2.fake.On("bcachefs", "subvolume", "list").Reply("Path ID\ndata 256\n")
	w2.fake.On("bcachefs", "set-file-option").Reply("")

	do2 := serveAPI(t, w2.harness)
	if resp, m := do2("PATCH", "/api/storage/subvolume", `{"path":"/var/mnt/pool/data","profile":"hot"}`); resp.Code != http.StatusConflict || m["code"] != "pool_not_tiered" {
		t.Fatalf("untiered profile: got %d %v", resp.Code, m)
	}
	if resp, m := do2("PATCH", "/api/storage/subvolume", `{"path":"/var/mnt/pool/data","copies":3}`); resp.Code != http.StatusConflict || m["code"] != "too_few_disks" {
		t.Fatalf("too many copies: got %d %v", resp.Code, m)
	}
	if resp, m := do2("PATCH", "/api/storage/subvolume", `{"path":"/var/mnt/pool/data","copies":2}`); resp.Code != http.StatusNoContent {
		t.Fatalf("untiered 2x copies: got %d %v", resp.Code, m)
	}

	// Old bcachefs tools version returns 409 image_too_old.
	w.mu.Lock()
	w.tools = "v1.31.0\n"
	w.mu.Unlock()
	do := serveAPI(t, w.harness)
	if resp, m := do("PATCH", "/api/storage/subvolume", `{"path":"/var/mnt/pool/tmp","copies":1}`); resp.Code != http.StatusConflict || m["code"] != "image_too_old" {
		t.Fatalf("old tools: got %d %v", resp.Code, m)
	}

	// Cached on a tiered pool whose superblock already sets fg=ssd, bg=hdd, promote=ssd clears overrides with `-`.
	w3 := newWorld(t, false, false)
	w3.disks = []*tdisk{
		{name: "sdf", rota: false, fstype: "bcachefs", label: "pool", uuid: poolUUID, mount: "/var/mnt/pool"},
		{name: "sde", rota: true, fstype: "bcachefs", label: "pool", uuid: poolUUID},
	}
	w3.fake.On("bcachefs", "show-super").Reply(
		"External UUID: " + poolUUID + "\nLabel: pool\n  data_replicas: 1\n" +
			"  foreground_target: ssd\n  background_target: hdd\n  promote_target: ssd\n" +
			"Device 0: /dev/sdf\n  Label: ssd.ssd1\n  State: rw\n" +
			"Device 1: /dev/sde\n  Label: hdd.hdd1\n  State: rw\n")
	w3.fake.On("bcachefs", "subvolume", "list").Reply("Path ID\ndata 256\n")
	w3.fake.On("bcachefs", "set-file-option").Reply("")
	if err := w3.svc.UpdateSubvolume(context.Background(), SubvolumeProfileRequest{Path: "/var/mnt/pool/data", Profile: ProfileCached}); err != nil {
		t.Fatal(err)
	}
	var setArgs string
	for _, c := range w3.fake.CallsMatching("bcachefs", "set-file-option") {
		setArgs = strings.Join(c.Args, " ")
	}
	if setArgs != "set-file-option --foreground_target=- --background_target=- --promote_target=- /var/mnt/pool/data" {
		t.Fatalf("clear cached args = %q", setArgs)
	}
}

// Sanity check that Status JSON encodes new fields cleanly.
func TestStorageStatusJSONRoundTrip(t *testing.T) {
	t.Parallel()
	st := StorageStatus{
		Pools: []Pool{{
			Usage: &PoolUsage{CachedBytes: 42, ReconcileMetadataBytes: 99},
			Disks: []Disk{{Usage: &DiskUsage{SizeBytes: 100, UsedBytes: 25, UsedPercent: 25}}},
		}},
		Subvolumes: []Subvolume{{Id: "1", Path: "/var/mnt/pool/a", UsedBytes: -1, Profile: ProfileHot, DataReplicas: 1}},
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var dec StorageStatus
	if err := json.Unmarshal(b, &dec); err != nil {
		t.Fatal(err)
	}
	if dec.Subvolumes[0].Profile != ProfileHot || dec.Subvolumes[0].DataReplicas != 1 || dec.Pools[0].Disks[0].Usage.UsedPercent != 25 || dec.Pools[0].Usage.CachedBytes != 42 {
		t.Fatalf("roundtrip = %+v", dec)
	}
}
