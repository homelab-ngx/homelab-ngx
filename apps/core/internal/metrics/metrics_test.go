package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeHost is a /proc, cgroup and /sys tree in a temp dir.
type fakeHost struct {
	t     *testing.T
	paths Paths
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	root := t.TempDir()
	h := &fakeHost{t: t, paths: Paths{
		Proc: filepath.Join(root, "proc"), Cgroup: filepath.Join(root, "cg"),
		Machines: filepath.Join(root, "cg", "machine.slice"), Net: filepath.Join(root, "net"),
		Services: filepath.Join(root, "cg", "system.slice"),
		Init:     filepath.Join(root, "cg", "init.scope"), Sessions: filepath.Join(root, "cg", "user.slice"),
		Hwmon: filepath.Join(root, "hwmon"), DevBlock: filepath.Join(root, "block"),
	}}
	h.write("proc/stat", "cpu  1 2 3\ncpu0 1 2 3\ncpu1 1 2 3\ncpu2 1 2 3\ncpu3 1 2 3\nbtime 1700000000\n")
	// Disks: sda is real, zram0 is not, nvme0n1p1 is a partition.
	h.mkdir("block/8:0/device")
	h.mkdir("block/253:0")
	h.mkdir("block/259:1/device")
	h.write("block/259:1/partition", "1\n")
	h.mkdir("net/eth0/device")
	h.mkdir("net/lo")
	h.write("hwmon/hwmon0/name", "nvme\n")
	h.write("hwmon/hwmon0/temp1_input", "41850\n")
	h.write("hwmon/hwmon1/name", "coretemp\n")
	h.write("hwmon/hwmon1/temp1_label", "Core 0\n")
	h.write("hwmon/hwmon1/temp1_input", "90000\n")
	h.write("hwmon/hwmon1/temp2_label", "Package id 0\n")
	h.write("hwmon/hwmon1/temp2_input", "55000\n")
	return h
}

func (h *fakeHost) path(rel string) string {
	return filepath.Join(filepath.Dir(h.paths.Proc), rel)
}

func (h *fakeHost) mkdir(rel string) {
	h.t.Helper()
	if err := os.MkdirAll(h.path(rel), 0o755); err != nil {
		h.t.Fatal(err)
	}
}

func (h *fakeHost) write(rel, body string) {
	h.t.Helper()
	h.mkdir(filepath.Dir(rel))
	if err := os.WriteFile(h.path(rel), []byte(body), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// state sets every counter. Units are µs for CPU and pressure, bytes for
// the rest.
type state struct {
	hostCPU, hostPSI, hostDisk, hostNet uint64
	memAvailKB                          uint64
	stackCPU, stackPSI, stackDisk       uint64
	stackMem, stackInactive             uint64
	vethRx, vethTx                      uint64 // host end
	unitCPU, unitMem                    uint64
	// The parts of core. svcVmalloc stands for the btree cache the daemon,
	// inside system.slice, was charged for.
	svcCPU, svcDisk, svcMem, svcVmalloc uint64
	sessCPU, sessMem                    uint64
}

func (h *fakeHost) set(s state) {
	psi := func(v uint64) string {
		return fmt.Sprintf("some avg10=0.00 avg60=0.00 avg300=0.00 total=%d\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n", v)
	}
	h.write("cg/cpu.stat", fmt.Sprintf("usage_usec %d\nuser_usec 1\n", s.hostCPU))
	for _, f := range []string{"cpu", "memory", "io"} {
		h.write("cg/"+f+".pressure", psi(s.hostPSI))
	}
	h.write("cg/io.stat", fmt.Sprintf("253:0 rbytes=999999 wbytes=999999\n8:0 rbytes=%d wbytes=%d rios=1\n259:0 rbytes=0 wbytes=0\n", s.hostDisk, 2*s.hostDisk))
	h.write("proc/meminfo", fmt.Sprintf("MemTotal:       16000000 kB\nMemFree:  1 kB\nMemAvailable:   %d kB\n", s.memAvailKB))
	h.write("proc/net/dev", fmt.Sprintf("Inter-|   Receive\n face |bytes packets errs drop fifo frame compressed multicast|bytes\n"+
		"    lo: 5000 0 0 0 0 0 0 0 5000 0 0 0 0 0 0 0\n eth0: %d 0 0 0 0 0 0 0 %d 0 0 0 0 0 0 0\n", s.hostNet, s.hostNet/2))

	scope := "cg/machine.slice/immich.scope/"
	h.write(scope+"cpu.stat", fmt.Sprintf("usage_usec %d\n", s.stackCPU))
	h.write(scope+"memory.current", fmt.Sprintf("%d\n", s.stackMem))
	h.write(scope+"memory.stat", fmt.Sprintf("anon 1\ninactive_file %d\n", s.stackInactive))
	for _, f := range []string{"cpu", "memory", "io"} {
		h.write(scope+f+".pressure", psi(s.stackPSI))
	}
	h.write(scope+"io.stat", fmt.Sprintf("8:0 rbytes=%d wbytes=0\n", s.stackDisk))
	h.write("net/ve-immich/statistics/rx_bytes", fmt.Sprintf("%d\n", s.vethRx))
	h.write("net/ve-immich/statistics/tx_bytes", fmt.Sprintf("%d\n", s.vethTx))
	unit := scope + "payload/system.slice/immich-server.service/"
	h.write(unit+"cpu.stat", fmt.Sprintf("usage_usec %d\n", s.unitCPU))
	h.write(unit+"memory.current", fmt.Sprintf("%d\n", s.unitMem))

	cgroup := func(dir string, cpu, mem, vmalloc, disk uint64) {
		h.write(dir+"cpu.stat", fmt.Sprintf("usage_usec %d\n", cpu))
		h.write(dir+"memory.current", fmt.Sprintf("%d\n", mem))
		h.write(dir+"memory.stat", fmt.Sprintf("anon 1\ninactive_file 0\nvmalloc %d\n", vmalloc))
		h.write(dir+"io.stat", fmt.Sprintf("8:0 rbytes=%d wbytes=0\n", disk))
	}
	cgroup("cg/system.slice/", s.svcCPU, s.svcMem, s.svcVmalloc, s.svcDisk)
	cgroup("cg/init.scope/", 0, 0, 0, 0)
	cgroup("cg/user.slice/", s.sessCPU, s.sessMem, 0, 0)
}

type oneStack struct{}

func (oneStack) Scopes(context.Context) []Scope {
	return []Scope{{Name: "immich", Units: []string{"immich-server.service"}}, {Name: "stopped"}}
}

func near(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.01*math.Max(1, math.Abs(want)) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestTickTurnsCountersIntoRates(t *testing.T) {
	t.Parallel()
	h := newFakeHost(t)
	m := New(h.paths, oneStack{}, nil, nil)
	now := time.Unix(1_700_000_000, 0)
	m.now = func() time.Time { return now }
	ctx := context.Background()

	h.set(state{memAvailKB: 12000000, stackMem: 1000, unitMem: 100})
	m.tick(ctx)
	if len(m.ring) != 0 || m.Usage("immich") != nil {
		t.Fatal("the first reading has no rates")
	}

	now = now.Add(2 * time.Second)
	h.set(state{
		hostCPU: 4_000_000, hostPSI: 200_000, hostDisk: 2000, hostNet: 4000, memAvailKB: 12000000,
		stackCPU: 1_000_000, stackPSI: 1_000_000, stackDisk: 500, stackMem: 3000, stackInactive: 1000,
		vethRx: 600, vethTx: 200, unitCPU: 500_000, unitMem: 600,
		svcCPU: 1_200_000, svcDisk: 400, svcMem: 3e9, svcVmalloc: 1.5e9,
		sessCPU: 800_000, sessMem: 1e9,
	})
	m.tick(ctx)
	if len(m.ring) != 1 {
		t.Fatalf("ring has %d samples", len(m.ring))
	}
	s := m.ring[0]
	// 4 core-seconds over 2 s on 4 cores is half the machine.
	near(t, "host cpu", s.host[figCPU], 50)
	near(t, "host memory", s.host[figMemory], 4000000*1024)
	near(t, "host pressure", s.host[figPressureIO], 10)
	near(t, "host disk read (zram left out)", s.host[figDiskRead], 1000)
	near(t, "host disk write", s.host[figDiskWrite], 2000)
	near(t, "host net rx (lo left out)", s.host[figNetRx], 2000)
	near(t, "cpu temp", s.host[figTempCPU], 55)
	near(t, "disk temp", s.host[figTempDisk], 41.85)

	st, ok := s.stacks["immich"]
	if !ok {
		t.Fatal("no immich point")
	}
	if _, ok := s.stacks["stopped"]; ok {
		t.Error("a stack without a cgroup has a point")
	}
	near(t, "stack cpu", st[figCPU], 12.5)
	near(t, "stack memory less inactive_file", st[figMemory], 2000)
	near(t, "stack pressure", st[figPressureCPU], 50)
	near(t, "stack disk", st[figDiskRead], 250)
	near(t, "stack rx is the host end's tx", st[figNetRx], 100)
	near(t, "stack tx is the host end's rx", st[figNetTx], 300)
	if !math.IsNaN(st[figTempCPU]) {
		t.Error("a stack has a temperature")
	}

	// system.slice 15% (the daemon among it); sessions 10%; the kernel is
	// the 12.5% left of the host's 50% after the stack's 12.5%.
	svc, sess, kernel := s.parts[partServices], s.parts[partSessions], s.parts[partKernel]
	near(t, "services cpu", svc[figCPU], 15)
	near(t, "sessions cpu", sess[figCPU], 10)
	near(t, "kernel cpu", kernel[figCPU], 12.5)
	// The 1.5 GB of vmalloc (btree cache the daemon was charged for) is the kernel's.
	near(t, "services memory less vmalloc", svc[figMemory], 1.5e9)
	near(t, "sessions memory", sess[figMemory], 1e9)
	near(t, "kernel memory", kernel[figMemory], 4000000*1024-2000-2.5e9)
	near(t, "services disk", svc[figDiskRead], 200)
	near(t, "kernel disk", kernel[figDiskRead], 550)
	for i, p := range s.parts {
		if !math.IsNaN(p[figPressureCPU]) || !math.IsNaN(p[figNetRx]) {
			t.Errorf("part %s has pressure or network", partScopes[i])
		}
	}

	if u := m.Usage("immich"); u == nil || math.Abs(float64(u.CpuPercent)-50) > 0.1 || u.MemoryBytes != 2000 {
		t.Errorf("stack usage = %+v (top-style: half a core is 50)", u)
	}
	if u := m.Usage("immich/immich-server.service"); u == nil || math.Abs(float64(u.CpuPercent)-25) > 0.1 || u.MemoryBytes != 600 {
		t.Errorf("unit usage = %+v", u)
	}
}

func TestMinutesAreStoredAndReadBack(t *testing.T) {
	t.Parallel()
	h := newFakeHost(t)
	ctx := context.Background()
	db, err := OpenDB(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := New(h.paths, oneStack{}, db, nil)
	start := time.Now().Truncate(time.Minute).Add(-5 * time.Minute)
	now := start.Add(54 * time.Second) // ticks at :54 :56 :58 and :00 of the next minute
	m.now = func() time.Time { return now }

	// 10% then 30% of the machine in the first minute, 20% in the next.
	var cpu uint64
	for _, pct := range []uint64{0, 10, 30, 20} {
		cpu += pct * 80_000 // pct% of 4 cores over 2 s
		h.set(state{hostCPU: cpu, memAvailKB: 12000000, stackCPU: cpu / 2, stackMem: 1000, sessCPU: cpu / 4})
		m.tick(ctx)
		now = now.Add(2 * time.Second)
	}
	m.flushMinute(ctx)
	// Rows of an older layout's parts are skipped.
	old := emptyPoint()
	old[figCPU] = 99
	if err := db.write(ctx, start, map[string]point{"@core": old, "@daemon": old}); err != nil {
		t.Fatal(err)
	}

	f, err := m.History(ctx, Range24h)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.At) != 2 || f.At[0] != start.Unix() || f.Step != 60 {
		t.Fatalf("at = %v step %d", f.At, f.Step)
	}
	near(t, "first minute's average", float64(f.Host.CPU[0]), 20)
	near(t, "first minute's highest", float64(f.Host.CPUMax[0]), 30)
	near(t, "second minute", float64(f.Host.CPU[1]), 20)
	if _, ok := f.Stacks["immich"]; !ok {
		t.Errorf("no immich in history: %v", f.Stacks)
	}
	if len(f.Stacks) != 1 {
		t.Errorf("an old part reads as a stack: %v", f.Stacks)
	}
	near(t, "sessions are stored", float64(f.Sessions.CPU[0]), 5)
	near(t, "the kernel is stored", float64(f.Kernel.CPU[0]), 5)

	// A restart fills the live ring from the stored minutes.
	m2 := New(h.paths, oneStack{}, db, nil)
	m2.now = func() time.Time { return now }
	m2.seed(ctx)
	snap, _, cancel := m2.Subscribe()
	cancel()
	if len(snap.At) != 2 || snap.Host.CPUMax != nil {
		t.Errorf("seeded snapshot = %+v", snap.At)
	}

	if err := db.prune(ctx, now); err != nil {
		t.Fatal(err)
	}
	if f, _ := m.History(ctx, Range24h); len(f.At) != 0 {
		t.Errorf("pruned history still has %v", f.At)
	}
}

func TestSubscribersGetEverySample(t *testing.T) {
	t.Parallel()
	h := newFakeHost(t)
	m := New(h.paths, nil, nil, nil)
	now := time.Unix(1_700_000_000, 0)
	m.now = func() time.Time { return now }
	ctx := context.Background()
	h.set(state{memAvailKB: 1})
	m.tick(ctx)
	_, ch, cancel := m.Subscribe()
	defer cancel()
	now = now.Add(2 * time.Second)
	h.set(state{hostCPU: 80_000, memAvailKB: 1})
	m.tick(ctx)
	select {
	case s := <-ch:
		near(t, "sample cpu", s.host[figCPU], 1)
	default:
		t.Fatal("no sample")
	}
}

func TestNumIsRoundedAndNullable(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal([]Num{Num(nan), 12.345, 3})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[null,12.3,3]" {
		t.Errorf("json = %s", b)
	}
}
