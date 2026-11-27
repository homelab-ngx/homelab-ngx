package bcachefs

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
)

func TestFormatArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec FormatSpec
		want string
	}{
		{"one kind", FormatSpec{Name: "pool", Copies: 2, Devices: []FormatDevice{{"/dev/sdb", "hdd.hdd1"}, {"/dev/sdc", "hdd.hdd2"}}},
			"format -q --fs_label=pool --replicas=2 --compression=lz4 --label=hdd.hdd1 /dev/sdb --label=hdd.hdd2 /dev/sdc"},
		{"both kinds, three copies", FormatSpec{Name: "tank", Copies: 3, Tiered: true, Devices: []FormatDevice{
			{"/dev/sda", "ssd.ssd1"}, {"/dev/sdb", "hdd.hdd1"}, {"/dev/sdc", "hdd.hdd2"}}},
			"format -q --fs_label=tank --replicas=3 --compression=lz4 --label=ssd.ssd1 /dev/sda --label=hdd.hdd1 /dev/sdb --label=hdd.hdd2 /dev/sdc " +
				"--foreground_target=ssd --promote_target=ssd --metadata_target=ssd --background_target=hdd"},
	} {
		if got := strings.Join(formatArgs(tc.spec), " "); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
	for _, a := range formatArgs(FormatSpec{Name: "p", Copies: 1, Devices: []FormatDevice{{"/dev/sdb", "hdd.hdd1"}}}) {
		if a == "-f" || a == "--force" {
			t.Fatal("format must not force: the tool's refusal of a used disk is a guard")
		}
	}
}

func TestParseSuper139(t *testing.T) {
	s := parseSuper(fixture(t, "show-super-1.39.txt"))
	if s.UUID != "1613240e-3f18-4688-910b-fdf327766cdb" || s.Label != "pool" {
		t.Fatalf("uuid/label = %q %q", s.UUID, s.Label)
	}
	if s.DataReplicas != 2 || s.MetadataReplicas != 2 {
		t.Fatalf("replicas = %d/%d", s.DataReplicas, s.MetadataReplicas)
	}
	if s.ForegroundTarget != "ssd" || s.BackgroundTarget != "hdd" || s.MetadataTarget != "ssd" || s.PromoteTarget != "ssd" {
		t.Fatalf("targets = %+v", s)
	}
	want := []ParsedDevice{
		{Index: "0", Path: "/dev/vdc", Model: "(unknown", Label: "ssd.ssd1", State: "rw"},
		{Index: "1", Path: "/dev/vdd", Model: "(unknown", Label: "hdd.hdd1", State: "rw"},
		{Index: "2", Path: "/dev/vdb1", Model: "(unknown", Label: "hdd.hdd2", State: "rw"},
	}
	if !reflect.DeepEqual(s.Devices, want) {
		t.Fatalf("devices = %+v", s.Devices)
	}
}

func TestParseDeviceUsage(t *testing.T) {
	rows := parseDeviceUsage(fixture(t, "fs-usage.txt"))
	want := []DeviceUsage{
		{Index: 2, Label: "hdd.hdd2", Name: "vdb1", State: "rw", Size: 17042501632, Used: 4308860928, UsedPercent: 25},
		{Index: 3, Label: "hdd.hdd3", Name: "vdb2", State: "rw", Size: 17042501632, Used: 4303093760, UsedPercent: 25},
		{Index: 0, Label: "ssd.ssd1", Name: "vdc", State: "rw", Size: 17042501632, Used: 4316987392, UsedPercent: 25},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %+v", rows)
	}

	rows = parseDeviceUsage(fixture(t, "fs-usage-evacuating.txt"))
	if len(rows) != 4 {
		t.Fatalf("rows = %+v", rows)
	}
	if r := rows[0]; r.Name != "vdd" || r.State != "evacuating" || r.Used != 4084465664 || r.UsedPercent != 23 || r.Leaving != 4082630656 {
		t.Fatalf("the leaving disk = %+v", r)
	}
	if rows[3].Leaving != 0 {
		t.Fatalf("a row with no Leaving figure = %+v", rows[3])
	}

	rows = parseDeviceUsage("(no label) (device 1):  vdb5    evacuating  17042501632   4523556864   26%  \n")
	if len(rows) != 1 || rows[0].Label != "" || rows[0].Index != 1 || rows[0].UsedPercent != 26 {
		t.Fatalf("an unlabelled disk = %+v", rows)
	}
}

func TestParseFSUsage(t *testing.T) {
	u := parseFSUsage(fixture(t, "fs-usage.txt"))
	if u.CachedBytes != 4294967296 || u.ReconcileData != 0 || u.ReconcileMeta != 22020096 || len(u.Devices) != 3 {
		t.Fatalf("fs-usage.txt = %+v", u)
	}

	multi := `Filesystem: f6f35d64-0b1c-4a6e-9a47-7a1d3c2e9b10
cached:  908380504064

Pending reconcile:  data    metadata
target:                0     1835008
pending:               0  5253103616

Device label          Device  State           Size           Used  Use%  Leaving
hdd.hdd1 (device 0):  sda     rw     3992193069056  1145140977664   29%  1835008
ssd.ssd1 (device 1):  sdb     rw     1016205406208   913506467840   92%
`
	got := parseFSUsage(multi)
	if got.CachedBytes != 908380504064 || got.ReconcileData != 0 || got.ReconcileMeta != 1835008+5253103616 {
		t.Fatalf("multi = %+v", got)
	}
	if len(got.Devices) != 2 || got.Devices[1].UsedPercent != 92 || got.Devices[0].Leaving != 1835008 {
		t.Fatalf("devices = %+v", got.Devices)
	}
}

func TestToolsVersion(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("bcachefs", "version").Reply("v1.39.7\n")
	major, minor, err := New(f).ToolsVersion(context.Background())
	if err != nil || major != 1 || minor != 39 {
		t.Fatalf("version = %d.%d, %v", major, minor, err)
	}
}

// sysTree builds a /sys/dev/block with a system disk whose root is a
// device-mapper device over two of its partitions, and returns it with a
// mountinfo that mounts / from the mapper and /var from a third partition.
func sysTree(t *testing.T) (sysDevBlock, mountinfo string) {
	t.Helper()
	root := t.TempDir()
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	link := func(target, name string) {
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
	}
	disk := mk("devices", "pci", "block", "nvme0n1")
	for _, p := range []string{"nvme0n1p2", "nvme0n1p3", "nvme0n1p4"} {
		dir := mk("devices", "pci", "block", "nvme0n1", p)
		if err := os.WriteFile(filepath.Join(dir, "partition"), []byte("2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dm := mk("devices", "virtual", "block", "dm-0")
	slaves := mk("devices", "virtual", "block", "dm-0", "slaves")
	link(filepath.Join(disk, "nvme0n1p2"), filepath.Join(slaves, "nvme0n1p2"))
	link(filepath.Join(disk, "nvme0n1p3"), filepath.Join(slaves, "nvme0n1p3"))
	other := mk("devices", "pci", "block", "sdz")

	sysDevBlock = mk("dev", "block")
	link(dm, filepath.Join(sysDevBlock, "254:0"))
	link(filepath.Join(disk, "nvme0n1p4"), filepath.Join(sysDevBlock, "259:4"))
	link(other, filepath.Join(sysDevBlock, "8:0"))

	mountinfo = filepath.Join(root, "mountinfo")
	body := "22 1 254:0 / / ro,relatime shared:1 - erofs /dev/mapper/root ro\n" +
		"30 22 259:4 / /var rw,relatime shared:2 - ext4 /dev/nvme0n1p4 rw\n" +
		"31 22 8:0 / /var/mnt/data rw,relatime shared:3 - ext4 /dev/sdz rw\n" +
		// A pool whose first disk, sdl, has left it: the mount keeps sdl's number.
		"40 22 8:176 / /var/mnt/tank rw,relatime shared:4 - bcachefs /dev/sdm:/dev/sdn rw\n"
	if err := os.WriteFile(mountinfo, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return sysDevBlock, mountinfo
}

func TestSystemDisks(t *testing.T) {
	c := New(cmdrunnertest.NewFake())
	c.StubHost(t.TempDir())
	c.sysDevBlock, c.mountinfo = sysTree(t)
	got := c.SystemDisks()
	if len(got) != 1 || !got["nvme0n1"] {
		t.Fatalf("system disks = %v, want only nvme0n1 (a data mount's disk is not one)", got)
	}
}

const candidatesLsblk = `{"blockdevices":[
 {"name":"nvme0n1","path":"/dev/nvme0n1","size":500000000000,"type":"disk","model":"System","rota":false,"ro":false,"mountpoints":[null]},
 {"name":"sda","path":"/dev/sda","size":4000000000000,"type":"disk","model":"Blank HDD","serial":"S1","rota":true,"ro":false,"mountpoints":[null]},
 {"name":"sdb","path":"/dev/sdb","size":1000000000000,"type":"disk","model":"Blank SSD","rota":false,"ro":false,"mountpoints":[null]},
 {"name":"sdc","path":"/dev/sdc","size":4000000000000,"type":"disk","fstype":"ext4","label":"Data","rota":true,"ro":false,"mountpoints":[null]},
 {"name":"sdd","path":"/dev/sdd","size":4000000000000,"type":"disk","rota":true,"ro":false,"mountpoints":[null],"children":[
   {"name":"sdd1","path":"/dev/sdd1","size":1000,"type":"part","fstype":"vfat","mountpoints":[null]},
   {"name":"sdd2","path":"/dev/sdd2","size":1000,"type":"part","fstype":"ntfs","mountpoints":[null]}]},
 {"name":"sde","path":"/dev/sde","size":4000000000000,"type":"disk","fstype":"bcachefs","label":"old","uuid":"aaaa-bbbb","rota":true,"ro":false,"mountpoints":[null]},
 {"name":"sdf","path":"/dev/sdf","size":4000000000000,"type":"disk","rota":true,"ro":false,"mountpoints":[null],"children":[
   {"name":"sdf1","path":"/dev/sdf1","size":1000,"type":"part","fstype":"ext4","mountpoints":["/var/mnt/media"]}]},
 {"name":"sdg","path":"/dev/sdg","size":4000000000000,"type":"disk","rota":true,"ro":false,"mountpoints":[null],"children":[
   {"name":"sdg1","path":"/dev/sdg1","size":1000,"type":"part","fstype":"swap","mountpoints":["[SWAP]"]}]},
 {"name":"sdh","path":"/dev/sdh","size":4000000000000,"type":"disk","rota":true,"ro":false,"mountpoints":[null],"children":[
   {"name":"sdh1","path":"/dev/sdh1","size":1000,"type":"part","fstype":"crypto_LUKS","mountpoints":[null],"children":[
     {"name":"secret","path":"/dev/mapper/secret","size":1000,"type":"crypt","mountpoints":[null]}]}]},
 {"name":"sdi","path":"/dev/sdi","size":4000000000000,"type":"disk","rota":true,"ro":true,"mountpoints":[null]},
 {"name":"sdj","path":"/dev/sdj","size":4000000000000,"type":"disk","rota":true,"ro":false,"mountpoints":[null]},
 {"name":"sdl","path":"/dev/sdl","size":4000000000000,"type":"disk","rota":true,"ro":false,"mountpoints":["/var/mnt/tank"]},
 {"name":"sdm","path":"/dev/sdm","size":4000000000000,"type":"disk","fstype":"bcachefs","rota":true,"ro":false,"mountpoints":["/var/mnt/tank"]},
 {"name":"sr0","path":"/dev/sr0","size":1000,"type":"rom","rota":false,"ro":false,"mountpoints":[null]},
 {"name":"sdk","path":"/dev/sdk","size":0,"type":"disk","model":"Card reader","rota":true,"ro":false,"mountpoints":[null]}
]}`

func TestCandidates(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("lsblk").Reply(candidatesLsblk)
	f.On("smartctl").Fail("no")
	// sdj: lsblk sees nothing, a backup superblock still answers.
	f.On("bcachefs", "show-super", "/dev/sdj").Reply("External UUID:   cccc-dddd\nLabel:   half\n")
	c := New(f)
	c.StubHost(t.TempDir())
	c.sysDevBlock, c.mountinfo = sysTree(t)

	list, err := c.Candidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ contents, kind, pool string }
	got := map[string]row{}
	for _, d := range list {
		got[d.Name] = row{d.Contents, Kind(d.Rotational), d.PoolUUID}
	}
	want := map[string]row{
		"sda": {"", "hdd", ""},
		"sdb": {"", "ssd", ""},
		"sdc": {`ext4 "Data"`, "hdd", ""},
		"sdd": {"2 partitions", "hdd", ""},
		"sde": {`bcachefs "old"`, "hdd", "aaaa-bbbb"},
		"sdj": {`bcachefs "half"`, "hdd", "cccc-dddd"},
		"sdl": {"", "hdd", ""}, // it left the pool mounted at /var/mnt/tank
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates:\n got %v\nwant %v\n(the system disk, a mounted partition, swap, LUKS, a read-only disk, a mounted pool's disk, an empty card reader and a rom are left out)", got, want)
	}
	for _, d := range list {
		if d.Name == "sdd" && !slices.Equal(d.Parts, []string{"/dev/sdd1", "/dev/sdd2"}) {
			t.Fatalf("sdd parts = %v", d.Parts)
		}
	}
}

func TestWipe(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("wipefs").Reply("")
	f.On("blockdev").Fail("no table")
	f.On("udevadm").Reply("")
	c := New(f)
	var zeroed []string
	c.zeroHead = func(path string, n int) error {
		if n != 4<<20 {
			t.Errorf("zeroed %d bytes of %s", n, path)
		}
		zeroed = append(zeroed, path)
		return nil
	}
	if err := c.Wipe(context.Background(), "/dev/sdd", []string{"/dev/sdd1", "/dev/sdd2"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/dev/sdd1", "/dev/sdd2", "/dev/sdd"}; !slices.Equal(zeroed, want) {
		t.Fatalf("zeroed %v, want the partitions then the disk", zeroed)
	}
	var wiped []string
	for _, call := range f.CallsMatching("wipefs") {
		if !slices.Equal(call.Args[:2], []string{"-a", "-q"}) {
			t.Fatalf("wipefs %v", call.Args)
		}
		wiped = append(wiped, call.Args[2])
	}
	if !slices.Equal(wiped, zeroed) {
		t.Fatalf("wipefs ran on %v", wiped)
	}

	// A disk something holds open is not touched further.
	c.zeroHead = func(string, int) error { return ErrDeviceBusy }
	f2 := cmdrunnertest.NewFake()
	c2 := New(f2)
	c2.zeroHead = c.zeroHead
	if err := c2.Wipe(context.Background(), "/dev/sdx", nil); err == nil || len(f2.Calls()) != 0 {
		t.Fatalf("a busy disk: err %v, commands %v", err, f2.Calls())
	}
}

func TestPoolCommands(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("bcachefs").Reply("")
	c := New(f)
	ctx := context.Background()
	if err := c.AddDevice(ctx, "/var/mnt/pool", "/dev/sdd", "hdd.hdd3"); err != nil {
		t.Fatal(err)
	}
	if err := c.Evacuate(ctx, "/dev/sdd"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveDevice(ctx, "/dev/sdd"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDeviceState(ctx, "/dev/sdd", "rw"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetTargets(ctx, "/dev/sda"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, call := range f.Calls() {
		got = append(got, strings.Join(call.Args, " "))
	}
	want := []string{
		"device add --label=hdd.hdd3 /var/mnt/pool /dev/sdd",
		"device evacuate /dev/sdd",
		"device remove /dev/sdd",
		"device set-state rw /dev/sdd",
		"set-fs-option --foreground_target=ssd /dev/sda",
		"set-fs-option --promote_target=ssd /dev/sda",
		"set-fs-option --metadata_target=ssd /dev/sda",
		"set-fs-option --background_target=hdd /dev/sda",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("commands:\n got %q\nwant %q", got, want)
	}
	for _, g := range got {
		if strings.Contains(g, " -f") || strings.Contains(g, " -F") || strings.Contains(g, "force") {
			t.Fatalf("a forced command: %s", g)
		}
	}
}
