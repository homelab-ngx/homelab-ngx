package bcachefs

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
)

const poolUUID = "f6f35d64-0b1c-4a6e-9a47-7a1d3c2e9b10"

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// newClient returns a Client over a scripted runner that answers lsblk,
// smartctl and show-super from the fixtures, with /proc/mounts pointed at a
// temp file.
func newClient(t *testing.T, mounts string) (*Client, *cmdrunnertest.Fake) {
	t.Helper()
	f := cmdrunnertest.NewFake()
	f.On("lsblk").Reply(fixture(t, "lsblk.json"))
	f.On("bcachefs", "show-super", "/dev/sda").Reply(fixture(t, "show-super.txt"))
	f.On("smartctl", "-j", "-i", "/dev/sda").Reply(fixture(t, "smartctl-ssd.json"))
	for _, d := range []string{"/dev/sdb", "/dev/sdc", "/dev/sdd"} {
		f.On("smartctl", "-j", "-i", d).Reply(fixture(t, "smartctl-hdd.json"))
	}
	c := New(f)
	c.procMounts = filepath.Join(t.TempDir(), "mounts")
	if err := os.WriteFile(c.procMounts, []byte(mounts), 0o644); err != nil {
		t.Fatal(err)
	}
	return c, f
}

func TestParseSuper(t *testing.T) {
	s := parseSuper(fixture(t, "show-super.txt"))
	if s.UUID != poolUUID || s.Label != "pool" {
		t.Fatalf("uuid/label = %q %q", s.UUID, s.Label)
	}
	if s.DataReplicas != 1 || s.MetadataReplicas != 2 {
		t.Fatalf("replicas = %d/%d", s.DataReplicas, s.MetadataReplicas)
	}
	if s.ForegroundTarget != "ssd" || s.BackgroundTarget != "hdd" || s.MetadataTarget != "ssd" || s.PromoteTarget != "" {
		t.Fatalf("targets = %+v", s)
	}
	if len(s.Devices) != 4 {
		t.Fatalf("devices = %+v", s.Devices)
	}
	want := []ParsedDevice{
		{"0", "sdb", "ST4000VN008", "hdd.hdd1", "rw"},
		{"1", "sda", "Samsung", "ssd.ssd1", "rw"},
		{"2", "sdc", "ST4000VN008", "hdd.hdd2", "rw"},
		{"3", "sdd", "ST4000VN008", "", "rw"}, // "(none)" normalised
	}
	for i, w := range want {
		if s.Devices[i] != w {
			t.Errorf("device %d = %+v, want %+v", i, s.Devices[i], w)
		}
	}
}

func TestParseSuperUnsetTargetsAreEmpty(t *testing.T) {
	s := parseSuper("External UUID: abc\n  foreground_target: none\n  background_target: none\n")
	if s.UUID != "abc" || s.ForegroundTarget != "" || s.BackgroundTarget != "" {
		t.Fatalf("%+v", s)
	}
	if s := parseSuper("garbage"); s.UUID != "" {
		t.Fatal("garbage must not yield a uuid")
	}
}

func TestDetermineDiskTargets(t *testing.T) {
	cases := []struct{ label, fg, meta, bg, data, metadata string }{
		{"ssd.ssd1", "ssd", "ssd", "hdd", "foreground", "foreground"},
		{"hdd.hdd1", "ssd", "ssd", "hdd", "background", ""},
		{"hdd.hdd1", "ssd", "", "", "", ""}, // nothing guessed when the superblock sets no background target
		{"", "ssd", "ssd", "hdd", "", ""},
		{"ssd", "ssd", "", "", "foreground", ""},
	}
	for _, c := range cases {
		d, m := determineDiskTargets(c.label, c.fg, c.meta, c.bg)
		if d != c.data || m != c.metadata {
			t.Errorf("%+v -> %q,%q", c, d, m)
		}
	}
}

func TestGetDisks(t *testing.T) {
	c, _ := newClient(t, "")
	disks, err := c.GetDisks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Disk{}
	for _, d := range disks {
		by[d.Name] = d
	}
	sda := by["sda"]
	if sda.Size != 1024209543168 || sda.FSType != "bcachefs" || sda.Model != "Samsung SSD 850 PRO 1TB" || sda.FriendlyName != "Samsung SSD 850 PRO 1TB" {
		t.Fatalf("sda = %+v", sda)
	}
	if by["sdb"].Mountpoint != "/pool" || by["sdb"].FriendlyName != "Seagate IronWolf ST4000VN008" {
		t.Fatalf("sdb = %+v", by["sdb"])
	}
	nv := by["nvme0n1"]
	if !nv.HasChildren || by["nvme0n1p1"].Mountpoint != "/boot" {
		t.Fatalf("nvme = %+v / %+v", nv, by["nvme0n1p1"])
	}
}

func TestFriendlyNameCachedAndFallsBack(t *testing.T) {
	c, f := newClient(t, "")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := c.GetDisks(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.CallsMatching("smartctl", "-j", "-i", "/dev/sda")); n != 1 {
		t.Fatalf("smartctl ran %d times for one device, want 1 (cached)", n)
	}
	// An unscripted device (smartctl fails) falls back to lsblk's model.
	if got := c.friendlyName(ctx, "/dev/sdz", "Fallback Model", 0); got != "Fallback Model" {
		t.Fatalf("fallback = %q", got)
	}
	if got := c.cachedFriendlyName(ctx, "", "M", 0); got != "M" {
		t.Fatalf("empty path = %q", got)
	}
}

func TestDiscoverPoolsMounted(t *testing.T) {
	mounts := "/dev/sdb:/dev/sda:/dev/sdc:/dev/sdd /pool bcachefs rw,nosuid 0 0\n" +
		"/dev/nvme0n1p2 /var ext4 rw 0 0\n"
	c, f := newClient(t, mounts)
	// A real kernel lists the uuid-source line for multi-device pools.
	if err := os.WriteFile(c.procMounts, []byte(strings.Replace(mounts, "/dev/sdb:/dev/sda:/dev/sdc:/dev/sdd", "/dev/disk/by-uuid/"+poolUUID, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	pools, err := c.DiscoverPools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 {
		t.Fatalf("pools = %+v", pools)
	}
	p := pools[0]
	if p.UUID != poolUUID || p.State != "mounted" || p.Mountdir != "/pool" {
		t.Fatalf("pool = %+v", p)
	}
	if p.Label != "pool" || p.DataReplicas != 1 || p.MetadataReplicas != 2 || p.ForegroundTarget != "ssd" || p.BackgroundTarget != "hdd" {
		t.Fatalf("pool super = %+v", p)
	}
	if len(p.Disks) != 4 {
		t.Fatalf("disks = %d", len(p.Disks))
	}
	got := map[string][2]string{}
	for _, d := range p.Disks {
		got[d.Name] = [2]string{d.DataTarget, d.BcachefsLabel}
	}
	if got["sda"] != [2]string{"foreground", "ssd.ssd1"} || got["sdb"] != [2]string{"background", "hdd.hdd1"} || got["sdd"] != [2]string{"", ""} {
		t.Fatalf("targets = %v", got)
	}
	// One show-super serves the whole pool (sdb-sdd reuse the cached parse).
	// The optical drive and the partitioned NVMe can't be members and are
	// never probed.
	for _, c := range f.CallsMatching("bcachefs", "show-super") {
		switch c.Args[1] {
		case "/dev/sdb", "/dev/sdc", "/dev/sdd":
			t.Errorf("redundant show-super for %s", c.Args[1])
		case "/dev/sr0", "/dev/nvme0n1":
			t.Errorf("show-super probed non-member %s", c.Args[1])
		}
	}
	for _, c := range f.CallsMatching("smartctl") {
		switch c.Args[len(c.Args)-1] {
		case "/dev/sr0", "/dev/zram0":
			t.Errorf("smartctl queried %s, which has no SMART data", c.Args[len(c.Args)-1])
		}
	}
}

func TestDiscoverPoolsUnmounted(t *testing.T) {
	c, _ := newClient(t, "")
	// lsblk fixture lists sdb at /pool: with no /proc/mounts entry it is still
	// reported mounted via lsblk's mountpoint (fallback).
	pools, err := c.DiscoverPools(context.Background())
	if err != nil || len(pools) != 1 {
		t.Fatalf("pools=%v err=%v", pools, err)
	}
	if pools[0].State != "mounted" || pools[0].Mountdir != "/pool" {
		t.Fatalf("pool = %+v", pools[0])
	}
}

func TestMountUnmount(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("mount").Reply("")
	f.On("umount").Reply("")
	c := New(f)
	ctx := context.Background()
	if err := c.Mount(ctx, nil, "/pool"); err == nil {
		t.Fatal("no devices must fail")
	}
	if err := c.Mount(ctx, []string{"/dev/sdb", "/dev/sda"}, "/pool"); err != nil {
		t.Fatal(err)
	}
	m := f.CallsMatching("mount")
	if len(m) != 1 || strings.Join(m[0].Args, " ") != "-t bcachefs /dev/sdb:/dev/sda /pool" {
		t.Fatalf("mount calls = %+v", m)
	}
	if err := c.Unmount(ctx, "/pool"); err != nil {
		t.Fatal(err)
	}
	if len(f.CallsMatching("umount", "/pool")) != 1 {
		t.Fatal("umount not run")
	}
	f2 := cmdrunnertest.NewFake()
	f2.On("umount").Fail("target is busy")
	if err := New(f2).Unmount(ctx, "/pool"); err == nil {
		t.Fatal("expected failure")
	}
}

func TestListSubvolumes(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("bcachefs", "subvolume", "list", "/pool").Reply(fixture(t, "subvolume-list.txt"))
	subs, err := New(f).ListSubvolumes(context.Background(), "/pool")
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 5 || subs[0] != (Subvolume{ID: "2", Path: "/pool/media", UsedBytes: -1}) || subs[4].Path != "/pool/mail-archive" || subs[4].ID != "6" {
		t.Fatalf("subs = %+v", subs)
	}
	f2 := cmdrunnertest.NewFake()
	f2.On("bcachefs", "subvolume", "list").Fail("not a bcachefs filesystem")
	if _, err := New(f2).ListSubvolumes(context.Background(), "/x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCreateAndDeleteSubvolume(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("bcachefs", "subvolume").Reply("")
	c := New(f)
	ctx := context.Background()
	if err := c.CreateSubvolume(ctx, "/pool/new"); err != nil {
		t.Fatal(err)
	}
	if len(f.CallsMatching("bcachefs", "subvolume", "create", "/pool/new")) != 1 {
		t.Fatal("create not run")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := c.DeleteSubvolume(ctx, dir)
	if !errors.Is(err, ErrSubvolumeNotEmpty) {
		t.Fatalf("err = %v", err)
	}
	if len(f.CallsMatching("bcachefs", "subvolume", "delete")) != 0 {
		t.Fatal("non-empty subvolume must never reach the delete command")
	}
	empty := t.TempDir()
	if err := c.DeleteSubvolume(ctx, empty); err != nil {
		t.Fatal(err)
	}
	if len(f.CallsMatching("bcachefs", "subvolume", "delete", empty)) != 1 {
		t.Fatal("delete not run for empty subvolume")
	}
}

func TestFolderSizes(t *testing.T) {
	f := cmdrunnertest.NewFake()
	c := New(f)
	c.procMounts = filepath.Join(t.TempDir(), "mounts")
	mounts := "/dev/sda:/dev/sdb /pool bcachefs rw 0 0\n" +
		"/dev/sdc /fast bcachefs rw 0 0\n" +
		"/dev/nvme0n1p2 / ext4 rw 0 0\n"
	if err := os.WriteFile(c.procMounts, []byte(mounts), 0o644); err != nil {
		t.Fatal(err)
	}
	f.On("ionice", "-c3", "nice", "-n19", "bcdu", "--json", "-d", "2", "/pool").
		Reply(`{"folders":[{"path":"/pool","total":9},{"path":"/pool/media","total":5},` +
			`{"path":"/pool/media/library","total":3},{"path":"/pool/immich","total":4}]}`)
	f.On("ionice", "-c3", "nice", "-n19", "bcdu", "--json", "-d", "0", "/fast/vm").Fail("not root")

	got, err := c.FolderSizes(context.Background(), []string{"/pool/media/library", "/pool/immich/", "/pool/gone", "/fast/vm"})
	if err == nil || !strings.Contains(err.Error(), "bcdu /fast/vm") {
		t.Fatalf("err = %v, want the /fast run's", err)
	}
	want := map[string]int64{"/pool/media/library": 3, "/pool/immich": 4}
	if !maps.Equal(got, want) {
		t.Fatalf("sizes = %v, want %v", got, want)
	}
	if n := len(f.CallsMatching("ionice")); n != 2 {
		t.Fatalf("bcdu ran %d times, want once per pool", n)
	}
}

func TestCommonParent(t *testing.T) {
	for _, c := range []struct {
		paths []string
		root  string
		depth int
	}{
		{[]string{"/pool/media"}, "/pool/media", 0},
		{[]string{"/pool/media", "/pool/immich"}, "/pool", 1},
		{[]string{"/pool/media/library", "/pool/immich"}, "/pool", 2},
		{[]string{"/pool", "/pool/media"}, "/pool", 1},
		{[]string{"/a/x", "/b/y"}, "/", 2},
	} {
		if root, depth := commonParent(c.paths); root != c.root || depth != c.depth {
			t.Errorf("commonParent(%v) = %s, %d; want %s, %d", c.paths, root, depth, c.root, c.depth)
		}
	}
}

func TestGetPoolUsage(t *testing.T) {
	c := New(cmdrunnertest.NewFake())
	c.statfs = func(_ string, st *syscall.Statfs_t) error {
		st.Bsize, st.Blocks, st.Bfree, st.Bavail = 4096, 1000, 400, 350
		return nil
	}
	u, err := c.GetPoolUsage(context.Background(), "/pool")
	if err != nil {
		t.Fatal(err)
	}
	if u.TotalBytes != 4096000 || u.UsedBytes != 600*4096 || u.AvailableBytes != 350*4096 || u.UsedPercent != 60 {
		t.Fatalf("usage = %+v", u)
	}
	c.statfs = func(string, *syscall.Statfs_t) error { return syscall.ENOENT }
	if _, err := c.GetPoolUsage(context.Background(), "/x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetDiskIO(t *testing.T) {
	c := New(cmdrunnertest.NewFake())
	c.diskstats = filepath.Join(t.TempDir(), "diskstats")
	data := "   8       0 sda 100 0 2000 5 200 0 4000 9 3 11 12\n" +
		"   8      16 sdb 7 0 70 1 8 0 80 2 0 3 4\n" +
		"   8      32 short 1 2\n"
	if err := os.WriteFile(c.diskstats, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetDiskIO(context.Background(), []string{"sda", "short", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	want := DiskIOStats{DeviceName: "sda", ReadIOs: 100, ReadSectors: 2000, WriteIOs: 200, WriteSectors: 4000, IOInProgress: 3}
	if len(got) != 1 || got["sda"] != want {
		t.Fatalf("io = %+v", got)
	}
}

func TestParseDiskHealth(t *testing.T) {
	t.Parallel()
	nvme := `{"smart_status":{"passed":false},"temperature":{"current":44},"power_on_time":{"hours":900},` +
		`"nvme_smart_health_information_log":{"media_errors":7}}`
	h, err := parseDiskHealth(nvme)
	if err != nil || h.Passed || h.TemperatureC != 44 || h.PowerOnHours != 900 || h.BadSectors != 7 {
		t.Fatalf("nvme = %+v, %v", h, err)
	}
	if _, err := parseDiskHealth(`{"smartctl":{"exit_status":2}}`); !errors.Is(err, ErrNoReading) {
		t.Fatalf("standby err = %v", err)
	}
}

func TestShrinkBtreeCacheMakesOnePassOnEveryPool(t *testing.T) {
	c := New(cmdrunnertest.NewFake())
	c.sysfs = t.TempDir()
	var triggers []string
	for _, uuid := range []string{poolUUID, "0d9c1b2e-5f3a-4c7d-8e6f-1a2b3c4d5e6f"} {
		dir := filepath.Join(c.sysfs, uuid, "internal")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "trigger_btree_cache_shrink")
		if err := os.WriteFile(p, nil, 0o200); err != nil { // sysfs has it; write-only
			t.Fatal(err)
		}
		triggers = append(triggers, p)
	}
	if err := c.ShrinkBtreeCache(); err != nil {
		t.Fatal(err)
	}
	for _, p := range triggers {
		_ = os.Chmod(p, 0o600)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != btreeShrinkCount {
			t.Errorf("%s = %q, want %q", p, b, btreeShrinkCount)
		}
	}
}

func TestShrinkBtreeCacheWithNoPoolIsANoOp(t *testing.T) {
	c := New(cmdrunnertest.NewFake())
	c.sysfs = filepath.Join(t.TempDir(), "missing")
	if err := c.ShrinkBtreeCache(); err != nil {
		t.Fatal(err)
	}
}
