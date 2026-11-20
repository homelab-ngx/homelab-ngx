package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/storage/bcachefs"
)

// tdisk is one disk of a scripted machine.
type tdisk struct {
	name   string
	rota   bool
	fstype string
	label  string
	uuid   string
	mount  string
}

// world scripts lsblk, the bcachefs tool and the wipe for the pool tests.
type world struct {
	*harness
	mu     sync.Mutex
	disks  []*tdisk
	zeroed []string
	usage  string // what `bcachefs fs usage` prints
	tools  string // what `bcachefs version` prints
	meta   *memMeta
}

type memMeta struct {
	mu sync.Mutex
	m  map[string]string
}

func (m *memMeta) Meta(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.m[key]
	return v, ok, nil
}

func (m *memMeta) SetMeta(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m[key] = value
	return nil
}

func (w *world) disk(name string) *tdisk {
	for _, d := range w.disks {
		if d.name == name {
			return d
		}
	}
	return nil
}

func (w *world) lsblk() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var rows []string
	for _, d := range w.disks {
		q := func(s string) string {
			if s == "" {
				return "null"
			}
			return fmt.Sprintf("%q", s)
		}
		rows = append(rows, fmt.Sprintf(
			`{"name":%q,"path":"/dev/%s","size":4000,"type":"disk","model":"M","serial":"serial-%s","fstype":%s,"label":%s,"uuid":%s,"rota":%t,"ro":false,"mountpoint":%s,"mountpoints":[%s]}`,
			d.name, d.name, d.name, q(d.fstype), q(d.label), q(d.uuid), d.rota, q(d.mount), q(d.mount)))
	}
	return `{"blockdevices":[` + strings.Join(rows, ",") + `]}`
}

// usageRows prints the device table of `bcachefs fs usage`.
func usageRows(rows ...string) string {
	return "Device label          Device  State         Size        Used  Use%  Leaving  \n" + strings.Join(rows, "\n") + "\n"
}

// newWorld is the harness's four-disk pool (sda the SSD; sdb to sdd HDDs,
// mounted at /var/mnt/pool when mounted) beside three spare disks: sde a
// blank HDD, sdf a blank SSD, sdg an HDD with ext4 on it.
func newWorld(t *testing.T, withPool, mounted bool) *world {
	t.Helper()
	w := &world{harness: newHarness(t), meta: &memMeta{m: map[string]string{}}, tools: "v1.39.7\n"}
	if withPool {
		for _, n := range []string{"sda", "sdb", "sdc", "sdd"} {
			w.disks = append(w.disks, &tdisk{name: n, rota: n != "sda", fstype: "bcachefs", label: "pool", uuid: poolUUID})
		}
		if mounted {
			w.disk("sdb").mount = "/var/mnt/pool"
		}
	}
	w.disks = append(w.disks,
		&tdisk{name: "sde", rota: true},
		&tdisk{name: "sdf"},
		&tdisk{name: "sdg", rota: true, fstype: "ext4", label: "Data"})
	w.usage = usageRows(
		"hdd.hdd1 (device 0):  sdb     rw     4000  1000   25%  ",
		"ssd.ssd1 (device 1):  sda     rw     4000  1000   25%  ",
		"hdd.hdd2 (device 2):  sdc     rw     4000  1000   25%  ",
		"(no label) (device 3):  sdd     rw     4000  1000   25%  ")
	w.fake.On("lsblk").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		return cmdrunner.Result{Stdout: w.lsblk()}, nil
	})
	w.fake.On("bcachefs", "version").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		return cmdrunner.Result{Stdout: w.tools}, nil
	})
	w.fake.On("bcachefs", "fs", "usage").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return cmdrunner.Result{Stdout: w.usage}, nil
	})
	w.fake.On("wipefs").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if d := w.disk(strings.TrimPrefix(c.Args[2], "/dev/")); d != nil {
			d.fstype, d.label, d.uuid = "", "", ""
		}
		return cmdrunner.Result{}, nil
	})
	for _, name := range []string{"blockdev", "udevadm", "chattr", "umount"} {
		w.fake.On(name).Reply("")
	}
	w.svc.bc.StubZero(func(path string, _ int) error {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.zeroed = append(w.zeroed, path)
		return nil
	})
	w.svc.UseStore(w.meta)
	w.svc.rmdir = func(string) error { return nil }
	return w
}

// commands are the bcachefs, mount and wipefs commands that ran, as text.
func (w *world) commands(prefix ...string) []string {
	var out []string
	for _, c := range w.fake.CallsMatching(prefix...) {
		out = append(out, strings.Join(append([]string{c.Name}, c.Args...), " "))
	}
	return out
}

func TestCreatePool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t, false, false)
	const newUUID = "11111111-2222-3333-4444-555555555555"
	w.fake.On("bcachefs", "format").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		for _, n := range []string{"sde", "sdf", "sdg"} {
			d := w.disk(n)
			d.fstype, d.label, d.uuid = "bcachefs", "tank", newUUID
		}
		return cmdrunner.Result{}, nil
	})
	w.fake.On("bcachefs", "show-super").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if d := w.disk(strings.TrimPrefix(c.Args[1], "/dev/")); d == nil || d.fstype != "bcachefs" {
			return cmdrunner.Result{ExitCode: 1}, errors.New("not a bcachefs superblock")
		}
		return cmdrunner.Result{Stdout: "External UUID:  " + newUUID + "\nLabel:  tank\n" +
			"  metadata_replicas:  2\n  data_replicas:  2\n  foreground_target:  ssd\n  background_target:  hdd\n" +
			"Device 0:  /dev/sde  M\n  Label:  hdd.hdd1\n  State:  rw\n" +
			"Device 1:  /dev/sdf  M\n  Label:  ssd.ssd1\n  State:  rw\n" +
			"Device 2:  /dev/sdg  M\n  Label:  hdd.hdd2\n  State:  rw\n"}, nil
	})
	w.fake.On("mount").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.disk("sde").mount = "/var/mnt/tank"
		return cmdrunner.Result{}, nil
	})
	all := []string{"/dev/sdg", "/dev/sde", "/dev/sdf"}

	for _, c := range []struct {
		name    string
		pool    string
		disks   []string
		copies  int
		confirm string
		want    error
	}{
		{"a name that is no directory", "Tank!", all, 2, "", ErrMountName},
		{"more copies than disks", "tank", []string{"/dev/sde", "/dev/sdf"}, 3, "", ErrTooFewDisks},
		{"a disk that is not spare", "tank", []string{"/dev/sde", "/dev/nvme0n1"}, 1, "", ErrDiskNotEligible},
		{"contents without the name", "tank", all, 2, "", ErrConfirm},
		{"contents with another name", "tank", all, 2, "pool", ErrConfirm},
	} {
		if _, err := w.svc.CreatePool(ctx, c.pool, c.disks, c.copies, c.confirm); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if got := append(w.commands("bcachefs", "format"), w.commands("wipefs")...); len(got) != 0 || len(w.zeroed) != 0 {
		t.Fatalf("a refused create touched the disks: %v, zeroed %v", got, w.zeroed)
	}

	pool, err := w.svc.CreatePool(ctx, "tank", all, 2, "tank")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.zeroed, []string{"/dev/sdg"}) {
		t.Fatalf("zeroed %v, want only the disk that had contents", w.zeroed)
	}
	want := "bcachefs format -q --fs_label=tank --replicas=2 --compression=lz4 " +
		"--label=hdd.hdd1 /dev/sde --label=ssd.ssd1 /dev/sdf --label=hdd.hdd2 /dev/sdg " +
		"--foreground_target=ssd --promote_target=ssd --metadata_target=ssd --background_target=hdd"
	if got := w.commands("bcachefs", "format"); len(got) != 1 || got[0] != want {
		t.Fatalf("format:\n got %v\nwant %s", got, want)
	}
	if got := w.commands("mount"); len(got) != 1 || got[0] != "mount -t bcachefs /dev/sde:/dev/sdf:/dev/sdg /var/mnt/tank" {
		t.Fatalf("mount = %v", got)
	}
	if pool.Uuid != newUUID || pool.Name != "tank" || pool.State != Mounted || pool.Mountdir != "/var/mnt/tank" || !pool.AutoMount || len(pool.Disks) != 3 {
		t.Fatalf("pool = %+v", pool)
	}
	if kinds := []string{pool.Disks[0].Kind, pool.Disks[1].Kind}; !slices.Equal(kinds, []string{"hdd", "ssd"}) {
		t.Fatalf("kinds = %v", kinds)
	}
	if sp, ok := w.cfg.Current().Pool(newUUID); !ok || sp.Mountpoint != "/var/mnt/tank" || !sp.AutoMount {
		t.Fatalf("stored pool = %+v %v", sp, ok)
	}
	st, err := w.svc.Status(ctx)
	if err != nil || len(st.Unassigned) != 0 {
		t.Fatalf("spare disks after the create = %+v, %v", st.Unassigned, err)
	}
	if _, err := w.svc.CreatePool(ctx, "tank", []string{"/dev/sde"}, 1, ""); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("a second pool of the name: %v", err)
	}
}

func TestSpareDisksInStatus(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true, true)
	st, err := w.svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range st.Unassigned {
		got = append(got, d.Name+" "+d.Kind+" "+d.Contents)
	}
	if want := []string{"sde hdd ", "sdf ssd ", `sdg hdd ext4 "Data"`}; !slices.Equal(got, want) {
		t.Fatalf("spare = %q, want %q (the pool's own disks are not spare)", got, want)
	}
	if d := st.Pools[0].Disks[1]; d.Name != "sdb" || d.State != "rw" || d.Kind != "hdd" {
		t.Fatalf("a member = %+v", d)
	}
}

func TestAddDisk(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t, true, true)
	w.fake.On("bcachefs", "device", "add").Reply("")

	if err := w.svc.AddDisk(ctx, "nope", "/dev/sde", ""); !errors.Is(err, ErrUnknownPool) {
		t.Fatalf("unknown pool: %v", err)
	}
	if err := w.svc.AddDisk(ctx, poolUUID, "/dev/sdc", ""); !errors.Is(err, ErrDiskNotEligible) {
		t.Fatalf("a disk of the pool itself: %v", err)
	}
	if err := w.svc.AddDisk(ctx, poolUUID, "/dev/sdg", ""); !errors.Is(err, ErrConfirm) {
		t.Fatalf("contents without the name: %v", err)
	}
	if len(w.commands("bcachefs", "device")) != 0 || len(w.zeroed) != 0 {
		t.Fatalf("a refused add ran %v, zeroed %v", w.commands("bcachefs", "device"), w.zeroed)
	}

	if err := w.svc.AddDisk(ctx, poolUUID, "/dev/sde", ""); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.AddDisk(ctx, poolUUID, "/dev/sdg", "pool"); err != nil {
		t.Fatal(err)
	}
	// The fixture's members are hdd1, ssd1, hdd2 and one with no label.
	want := []string{
		"bcachefs device add --label=hdd.hdd3 /var/mnt/pool /dev/sde",
		"bcachefs device add --label=hdd.hdd3 /var/mnt/pool /dev/sdg",
	}
	if got := w.commands("bcachefs", "device"); !slices.Equal(got, want) {
		t.Fatalf("commands = %q", got)
	}
	if !slices.Equal(w.zeroed, []string{"/dev/sdg"}) {
		t.Fatalf("zeroed %v", w.zeroed)
	}
	if got := w.commands("bcachefs", "set-fs-option"); len(got) != 0 {
		t.Fatalf("a pool that has targets got them again: %v", got)
	}

	w.mu.Lock()
	w.usage += "hdd.hdd9 (device 9):  sdz     rw     4000  1000   25%  \n"
	w.mu.Unlock()
	if err := w.svc.AddDisk(ctx, poolUUID, "/dev/sdf", ""); !errors.Is(err, ErrPoolDegraded) {
		t.Fatalf("a pool that counts a disk that is not here: %v", err)
	}

	u := newWorld(t, true, false)
	if err := u.svc.AddDisk(ctx, poolUUID, "/dev/sde", ""); !errors.Is(err, ErrPoolState) {
		t.Fatalf("an unmounted pool: %v", err)
	}
}

func TestAddDiskMakesThePoolTiered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t, false, false)
	const uuid = "99999999-2222-3333-4444-555555555555"
	e := w.disk("sde")
	e.fstype, e.label, e.uuid, e.mount = "bcachefs", "hdds", uuid, "/var/mnt/hdds"
	w.fake.On("bcachefs", "show-super", "/dev/sde").Reply("External UUID:  " + uuid + "\nLabel:  hdds\n" +
		"  metadata_replicas:  1\n  data_replicas:  1\n  foreground_target:  none\n  background_target:  none\n" +
		"Device 0:  /dev/sde  M\n  Label:  hdd.hdd1\n  State:  rw\n")
	w.usage = usageRows("hdd.hdd1 (device 0):  sde     rw     4000  1000   25%  ")
	w.fake.On("bcachefs", "device", "add").Reply("")
	w.fake.On("bcachefs", "set-fs-option").Reply("")

	if err := w.svc.AddDisk(ctx, uuid, "/dev/sdf", ""); err != nil {
		t.Fatal(err)
	}
	if got := w.commands("bcachefs", "device", "add"); len(got) != 1 || got[0] != "bcachefs device add --label=ssd.ssd1 /var/mnt/hdds /dev/sdf" {
		t.Fatalf("add = %v", got)
	}
	if got := w.commands("bcachefs", "set-fs-option"); len(got) != 4 || got[3] != "bcachefs set-fs-option --background_target=hdd /dev/sde" {
		t.Fatalf("targets = %q", got)
	}
}

func TestUsableRaw(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		sizes  []uint64
		copies int
		want   uint64
	}{
		{[]uint64{4, 4, 4}, 1, 12},
		{[]uint64{4, 4, 4}, 2, 12},
		{[]uint64{10, 2, 2}, 2, 8}, // the large disk pairs with 4 on the others
		{[]uint64{10, 1}, 2, 2},
		{[]uint64{6, 6, 6}, 3, 18},
		{[]uint64{4}, 2, 0},
	} {
		if got := usableRaw(c.sizes, c.copies); got != c.want {
			t.Errorf("usableRaw(%v, %d) = %d, want %d", c.sizes, c.copies, got, c.want)
		}
	}
}

func TestRemovable(t *testing.T) {
	t.Parallel()
	disk := func(name, label string) bcachefs.Disk {
		return bcachefs.Disk{Name: name, Path: "/dev/" + name, BcachefsLabel: label}
	}
	use := func(name string, size, used uint64) bcachefs.DeviceUsage {
		return bcachefs.DeviceUsage{Name: name, Size: size, Used: used}
	}
	pool := bcachefs.Pool{
		DataReplicas: 2, MetadataReplicas: 2, ForegroundTarget: "ssd", BackgroundTarget: "hdd",
		Disks: []bcachefs.Disk{disk("sda", "ssd.ssd1"), disk("sdb", "hdd.hdd1"), disk("sdc", "hdd.hdd2"), disk("sdd", "hdd.hdd3")},
	}
	light := []bcachefs.DeviceUsage{use("sda", 1000, 100), use("sdb", 4000, 500), use("sdc", 4000, 500), use("sdd", 4000, 500)}
	full := []bcachefs.DeviceUsage{use("sda", 1000, 900), use("sdb", 4000, 3000), use("sdc", 4000, 3000), use("sdd", 4000, 3000)}

	if err := removable(pool, light, pool.Disks[1]); err != nil {
		t.Errorf("an HDD of three, with room: %v", err)
	}
	if err := removable(pool, light, pool.Disks[0]); !errors.Is(err, ErrLastOfRole) {
		t.Errorf("the only SSD: %v", err)
	}
	if err := removable(pool, full, pool.Disks[1]); !errors.Is(err, ErrNoRoom) {
		t.Errorf("9900 bytes onto disks that take 9000: %v", err)
	}
	two := pool
	two.ForegroundTarget, two.BackgroundTarget = "", ""
	two.Disks = pool.Disks[1:3]
	if err := removable(two, light[1:3], two.Disks[0]); !errors.Is(err, ErrTooFewDisks) {
		t.Errorf("one of two disks with two copies: %v", err)
	}
}

// evacuation scripts `bcachefs device evacuate` to wait for end, and to
// answer what end receives.
func (w *world) evacuation() chan error {
	end := make(chan error, 1)
	w.fake.On("bcachefs", "device", "evacuate").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		if err := <-end; err != nil {
			return cmdrunner.Result{ExitCode: 1}, err
		}
		return cmdrunner.Result{}, nil
	})
	w.fake.On("bcachefs", "device", "remove").Reply("")
	w.fake.On("bcachefs", "device", "set-state").Reply("")
	return end
}

func (w *world) waitOp(t *testing.T, what string, ok func(*PoolOperation) bool) *PoolOperation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		op := w.svc.operation(poolUUID)
		if ok(op) {
			return op
		}
		if time.Now().After(deadline) {
			t.Fatalf("no operation that is %s; last %+v", what, op)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestRemoveDisk(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t, true, true)
	w.svc.usageEvery = 2 * time.Millisecond
	end := w.evacuation()

	if _, err := w.svc.RemoveDisk(ctx, poolUUID, "/dev/sde"); !errors.Is(err, ErrUnknownDisk) {
		t.Fatalf("a disk that is not in the pool: %v", err)
	}
	if _, err := w.svc.RemoveDisk(ctx, poolUUID, "/dev/sda"); !errors.Is(err, ErrLastOfRole) {
		t.Fatalf("the only SSD: %v", err)
	}

	pool, err := w.svc.RemoveDisk(ctx, poolUUID, "/dev/sdc")
	if err != nil {
		t.Fatal(err)
	}
	if op := pool.Operation; op == nil || op.Kind != "remove" || op.Disk != "/dev/sdc" || op.Phase != "moving" || op.TotalBytes != 1000 || op.FinishedAt != nil {
		t.Fatalf("operation = %+v", pool.Operation)
	}
	if !strings.Contains(w.meta.m[removalsKey], `"disk":"/dev/sdc"`) {
		t.Fatalf("the removal is not stored: %q", w.meta.m[removalsKey])
	}

	// While it runs nothing else changes the pool.
	if err := w.svc.AddDisk(ctx, poolUUID, "/dev/sde", ""); !errors.Is(err, ErrBusy) {
		t.Errorf("add during a removal: %v", err)
	}
	if _, err := w.svc.RemoveDisk(ctx, poolUUID, "/dev/sdd"); !errors.Is(err, ErrBusy) {
		t.Errorf("a second removal: %v", err)
	}
	if err := w.svc.DeletePool(ctx, poolUUID, "pool"); !errors.Is(err, ErrBusy) {
		t.Errorf("delete during a removal: %v", err)
	}
	if err := w.svc.Unmount(ctx, "/var/mnt/pool"); !errors.Is(err, ErrBusy) {
		t.Errorf("unmount during a removal: %v", err)
	}

	w.mu.Lock()
	w.usage = usageRows(
		"hdd.hdd1 (device 0):  sdb     rw     4000  1300   25%  ",
		"ssd.ssd1 (device 1):  sda     rw     4000  1000   25%  ",
		"hdd.hdd2 (device 2):  sdc     evacuating     4000  400   10%  400",
		"(no label) (device 3):  sdd     rw     4000  1300   25%  ")
	w.mu.Unlock()
	w.waitOp(t, "at 400 bytes left", func(op *PoolOperation) bool { return op != nil && op.LeftBytes == 400 })

	end <- nil
	op := w.waitOp(t, "finished", func(op *PoolOperation) bool { return op != nil && op.FinishedAt != nil })
	if op.Phase != "done" || op.Error != "" || op.LeftBytes != 0 {
		t.Fatalf("operation = %+v", op)
	}
	want := []string{"bcachefs device evacuate /dev/sdc", "bcachefs device remove /dev/sdc"}
	if got := w.commands("bcachefs", "device"); !slices.Equal(got, want) {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	if !slices.Equal(w.zeroed, []string{"/dev/sdc"}) || len(w.commands("wipefs")) != 1 {
		t.Fatalf("the removed disk is not wiped: zeroed %v, wipefs %v", w.zeroed, w.commands("wipefs"))
	}
	if v := w.meta.m[removalsKey]; v != "{}" {
		t.Fatalf("the stored removal stays: %q", v)
	}
	// A removal that ended is cleared from the status.
	if err := w.svc.CancelRemoval(poolUUID); err != nil || w.svc.operation(poolUUID) != nil {
		t.Fatalf("clearing a finished removal: %v, %+v", err, w.svc.operation(poolUUID))
	}
}

func TestRemoveDiskCancelAndStall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("cancel", func(t *testing.T) {
		w := newWorld(t, true, true)
		end := w.evacuation()
		if _, err := w.svc.RemoveDisk(ctx, poolUUID, "/dev/sdc"); err != nil {
			t.Fatal(err)
		}
		if err := w.svc.CancelRemoval(poolUUID); err != nil {
			t.Fatal(err)
		}
		end <- errors.New("signal: killed")
		w.waitOp(t, "gone", func(op *PoolOperation) bool { return op == nil })
		if got := w.commands("bcachefs", "device", "set-state"); len(got) != 1 || got[0] != "bcachefs device set-state rw /dev/sdc" {
			t.Fatalf("the disk is not given back: %v", got)
		}
		if len(w.commands("bcachefs", "device", "remove")) != 0 || len(w.zeroed) != 0 {
			t.Fatal("a cancelled removal removed or wiped the disk")
		}
		if v := w.meta.m[removalsKey]; v != "{}" {
			t.Fatalf("the stored removal stays: %q", v)
		}
	})

	t.Run("the tool fails", func(t *testing.T) {
		w := newWorld(t, true, true)
		end := w.evacuation()
		if _, err := w.svc.RemoveDisk(ctx, poolUUID, "/dev/sdc"); err != nil {
			t.Fatal(err)
		}
		end <- errors.New("filesystem needs version_upgrade=incompatible")
		op := w.waitOp(t, "failed", func(op *PoolOperation) bool { return op != nil && op.FinishedAt != nil })
		if op.Phase != "failed" || !strings.Contains(op.Error, "version_upgrade") {
			t.Fatalf("operation = %+v", op)
		}
		if len(w.commands("bcachefs", "device", "remove")) != 0 || len(w.zeroed) != 0 {
			t.Fatal("a failed evacuation still removed or wiped the disk")
		}
	})

	t.Run("no progress", func(t *testing.T) {
		w := newWorld(t, true, true)
		w.svc.usageEvery, w.svc.stallAfter = 2*time.Millisecond, 20*time.Millisecond
		w.usage = usageRows(
			"hdd.hdd1 (device 0):  sdb     rw     4000  1000   25%  ",
			"ssd.ssd1 (device 1):  sda     rw     4000  1000   25%  ",
			"hdd.hdd2 (device 2):  sdc     rw     4000  1000   25%  1000",
			"(no label) (device 3):  sdd     rw     4000  1000   25%  ")
		w.fake.On("bcachefs", "device", "evacuate").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
			time.Sleep(150 * time.Millisecond) // the tool waits on; it is killed
			return cmdrunner.Result{ExitCode: -1}, errors.New("signal: killed")
		})
		w.fake.On("bcachefs", "device", "set-state").Reply("")
		if _, err := w.svc.RemoveDisk(ctx, poolUUID, "/dev/sdc"); err != nil {
			t.Fatal(err)
		}
		op := w.waitOp(t, "failed", func(op *PoolOperation) bool { return op != nil && op.FinishedAt != nil })
		if op.Phase != "failed" || !strings.Contains(op.Error, "the other disks are full") {
			t.Fatalf("operation = %+v", op)
		}
		if got := w.commands("bcachefs", "device", "set-state"); len(got) != 1 {
			t.Fatalf("the disk is not given back: %v", got)
		}
	})
}

func TestResumeRemovals(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	rec := func(disk string) string {
		b, _ := json.Marshal(map[string]removal{poolUUID: {
			PoolUUID: poolUUID, Mountpoint: "/var/mnt/pool", Disk: "/dev/" + disk, Name: disk,
			Serial: "serial-sdc", Total: 1000, StartedAt: time.Now(),
		}})
		return string(b)
	}

	t.Run("still moving, under another path", func(t *testing.T) {
		w := newWorld(t, true, true)
		end := w.evacuation()
		w.meta.m[removalsKey] = rec("sdx") // sdc before the restart
		w.svc.ResumeRemovals(ctx)
		w.waitOp(t, "moving", func(op *PoolOperation) bool { return op != nil && op.Phase == "moving" && op.Disk == "/dev/sdc" })
		end <- nil
		w.waitOp(t, "done", func(op *PoolOperation) bool { return op != nil && op.Phase == "done" })
		if got := w.commands("bcachefs", "device", "evacuate"); len(got) != 1 || got[0] != "bcachefs device evacuate /dev/sdc" {
			t.Fatalf("evacuate = %v", got)
		}
	})

	t.Run("removed but not wiped", func(t *testing.T) {
		w := newWorld(t, true, true)
		w.evacuation()
		w.usage = usageRows(
			"hdd.hdd1 (device 0):  sdb     rw     4000  1000   25%  ",
			"ssd.ssd1 (device 1):  sda     rw     4000  1000   25%  ",
			"(no label) (device 3):  sdd     rw     4000  1000   25%  ")
		w.meta.m[removalsKey] = rec("sdc")
		w.svc.ResumeRemovals(ctx)
		if !slices.Equal(w.zeroed, []string{"/dev/sdc"}) || len(w.commands("bcachefs", "device")) != 0 {
			t.Fatalf("zeroed %v, commands %v", w.zeroed, w.commands("bcachefs", "device"))
		}
		if v := w.meta.m[removalsKey]; v != "{}" {
			t.Fatalf("the stored removal stays: %q", v)
		}
	})

	t.Run("the pool is not mounted", func(t *testing.T) {
		w := newWorld(t, true, false)
		w.meta.m[removalsKey] = rec("sdc")
		w.svc.ResumeRemovals(ctx)
		if op := w.svc.operation(poolUUID); op == nil || op.Phase != "failed" || !strings.Contains(op.Error, "not mounted") {
			t.Fatalf("operation = %+v", op)
		}
	})
}

type fakeUsers []string

func (u fakeUsers) PoolUsers(_ context.Context, mountpoint string) ([]string, error) {
	if mountpoint != "/var/mnt/pool" {
		return nil, fmt.Errorf("asked for %s", mountpoint)
	}
	return u, nil
}

func TestDeletePool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t, true, true)
	if _, err := w.cfg.SetPoolAutoMount(ctx, poolUUID, "/var/mnt/pool", true); err != nil {
		t.Fatal(err)
	}
	var removed []string
	w.svc.rmdir = func(path string) error { removed = append(removed, path); return nil }

	w.svc.UseUsers(fakeUsers{"the backup offsite", "the stack media"})
	err := w.svc.DeletePool(ctx, poolUUID, "pool")
	if !errors.Is(err, ErrPoolInUse) || !strings.Contains(err.Error(), "the backup offsite, the stack media") {
		t.Fatalf("a pool in use: %v", err)
	}
	w.svc.UseUsers(fakeUsers{})
	if err := w.svc.DeletePool(ctx, poolUUID, ""); !errors.Is(err, ErrConfirm) {
		t.Fatalf("no name: %v", err)
	}
	if err := w.svc.DeletePool(ctx, poolUUID, "tank"); !errors.Is(err, ErrConfirm) {
		t.Fatalf("another name: %v", err)
	}
	if err := w.svc.DeletePool(ctx, "nope", "pool"); !errors.Is(err, ErrUnknownPool) {
		t.Fatalf("unknown pool: %v", err)
	}
	if len(w.commands("umount")) != 0 || len(w.zeroed) != 0 {
		t.Fatalf("a refused delete ran umount %v, zeroed %v", w.commands("umount"), w.zeroed)
	}

	if err := w.svc.DeletePool(ctx, poolUUID, "pool"); err != nil {
		t.Fatal(err)
	}
	if got := w.commands("umount"); len(got) != 1 || got[0] != "umount /var/mnt/pool" {
		t.Fatalf("umount = %v", got)
	}
	if want := []string{"/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd"}; !slices.Equal(w.zeroed, want) {
		t.Fatalf("zeroed %v, want every member", w.zeroed)
	}
	if _, ok := w.cfg.Current().Pool(poolUUID); ok {
		t.Fatal("the pool is still stored")
	}
	if !slices.Equal(removed, []string{"/var/mnt/pool"}) {
		t.Fatalf("removed directories = %v", removed)
	}
}

func TestPoolProblems(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true, true)
	do := serveAPI(t, w.harness)
	for _, c := range []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"create with contents and no confirm", "POST", "/api/storage/pools", `{"name":"tank","disks":["/dev/sdg"],"copies":1}`, http.StatusUnprocessableEntity, "confirm"},
		{"create with more copies than disks", "POST", "/api/storage/pools", `{"name":"tank","disks":["/dev/sde"],"copies":2}`, http.StatusConflict, "too_few_disks"},
		{"create with four copies", "POST", "/api/storage/pools", `{"name":"tank","disks":["/dev/sde"],"copies":4}`, http.StatusUnprocessableEntity, "bad_request"},
		{"create on a member", "POST", "/api/storage/pools", `{"name":"tank","disks":["/dev/sda"],"copies":1}`, http.StatusConflict, "disk_not_eligible"},
		{"add to an unknown pool", "POST", "/api/storage/pools/nope/disks", `{"disk":"/dev/sde"}`, http.StatusNotFound, "unknown_pool"},
		{"remove a stranger", "DELETE", "/api/storage/pools/" + poolUUID + "/disks?disk=/dev/sde", "", http.StatusNotFound, "unknown_disk"},
		{"remove the only SSD", "DELETE", "/api/storage/pools/" + poolUUID + "/disks?disk=/dev/sda", "", http.StatusConflict, "last_of_role"},
		{"delete without the name", "DELETE", "/api/storage/pools/" + poolUUID, "", http.StatusUnprocessableEntity, "confirm"},
		{"cancel nothing", "DELETE", "/api/storage/pools/" + poolUUID + "/operation", "", http.StatusNotFound, "unknown_pool"},
	} {
		rec, m := do(c.method, c.path, c.body)
		if rec.Code != c.status || m["code"] != c.code {
			t.Errorf("%s: got %d %v, want %d %s: %s", c.name, rec.Code, m["code"], c.status, c.code, rec.Body)
		}
	}

	old := newWorld(t, true, true)
	old.tools = "v1.20.0\n"
	rec, m := serveAPI(t, old.harness)("POST", "/api/storage/pools/"+poolUUID+"/disks", `{"disk":"/dev/sde"}`)
	if rec.Code != http.StatusConflict || m["code"] != "image_too_old" {
		t.Errorf("old tools: got %d %v", rec.Code, m["code"])
	}
}
