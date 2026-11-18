package metrics

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Paths are where the sampler reads from. Every source is a file: the
// sampler never starts a process, so reading every 2 s stays cheap.
type Paths struct {
	Proc     string // /proc
	Cgroup   string // the cgroup v2 root; its files describe the whole host
	Machines string // machine.slice, holding <stack>.scope
	Services string // system.slice: the host's services, the daemon among them
	Init     string // init.scope: systemd itself
	Sessions string // user.slice: login sessions and their user services
	Net      string // /sys/class/net
	Hwmon    string // /sys/class/hwmon
	DevBlock string // /sys/dev/block
}

// DefaultPaths is the layout on the homelab host.
func DefaultPaths() Paths {
	return Paths{
		Proc: "/proc", Cgroup: "/sys/fs/cgroup", Machines: "/sys/fs/cgroup/machine.slice",
		Services: "/sys/fs/cgroup/system.slice", Init: "/sys/fs/cgroup/init.scope", Sessions: "/sys/fs/cgroup/user.slice",
		Net: "/sys/class/net", Hwmon: "/sys/class/hwmon", DevBlock: "/sys/dev/block",
	}
}

// counters is one reading of a cgroup (or the host): the cumulative counters
// a rate is the difference of, and memory, which is a level.
type counters struct {
	cpuUsec  uint64
	memory   float64
	psi      [3]uint64 // "some" stall time in µs: cpu, memory, io
	diskRead uint64
	diskW    uint64
	netRx    uint64
	netTx    uint64
	hasNet   bool
}

// readFile reads a small file; sysfs and cgroup files fit in one read.
func readFile(path string) ([]byte, bool) {
	b, err := os.ReadFile(path)
	return b, err == nil
}

// readUint reads a file holding one unsigned number.
func readUint(path string) (uint64, bool) {
	b, ok := readFile(path)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return v, err == nil
}

// keyed returns the value of `key value` lines (cpu.stat, memory.stat).
func keyed(b []byte, key string) (uint64, bool) {
	for line := range bytes.Lines(b) {
		k, v, ok := bytes.Cut(bytes.TrimSpace(line), []byte(" "))
		if ok && string(k) == key {
			n, err := strconv.ParseUint(string(v), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// pressureTotal is the "some" line's total= of a PSI file, in µs.
func pressureTotal(path string) uint64 {
	b, ok := readFile(path)
	if !ok {
		return 0
	}
	for line := range bytes.Lines(b) {
		if !bytes.HasPrefix(line, []byte("some ")) {
			continue
		}
		for f := range bytes.FieldsSeq(line) {
			if v, ok := bytes.CutPrefix(f, []byte("total=")); ok {
				n, _ := strconv.ParseUint(string(v), 10, 64)
				return n
			}
		}
	}
	return 0
}

// readCPU reads cpu.stat's usage_usec. The root cgroup's covers the host.
func readCPU(dir string) (uint64, bool) {
	b, ok := readFile(filepath.Join(dir, "cpu.stat"))
	if !ok {
		return 0, false
	}
	return keyed(b, "usage_usec")
}

// readPressure reads the cpu, memory and io stall totals of a cgroup.
func readPressure(dir string) [3]uint64 {
	return [3]uint64{
		pressureTotal(filepath.Join(dir, "cpu.pressure")),
		pressureTotal(filepath.Join(dir, "memory.pressure")),
		pressureTotal(filepath.Join(dir, "io.pressure")),
	}
}

// cgroupMemory is what a cgroup uses: memory.current less the page cache the
// kernel can drop at once (inactive_file), which is how `docker stats` and
// systemd-cgtop's "memory" read too. It also leaves out vmalloc: that is
// kernel cache charged to whoever made the kernel fill it, above all
// bcachefs's btree cache (a process walking the pool's btrees is charged
// for every node it read), so it counts as the kernel's.
func cgroupMemory(dir string) (float64, bool) {
	cur, ok := readUint(filepath.Join(dir, "memory.current"))
	if !ok {
		return 0, false
	}
	if b, ok := readFile(filepath.Join(dir, "memory.stat")); ok {
		for _, key := range []string{"inactive_file", "vmalloc"} {
			if v, ok := keyed(b, key); ok && v < cur {
				cur -= v
			}
		}
	}
	return float64(cur), true
}

// hostMemory is MemTotal − MemAvailable from /proc/meminfo, and MemTotal.
func hostMemory(proc string) (used, total float64, ok bool) {
	b, ok := readFile(filepath.Join(proc, "meminfo"))
	if !ok {
		return 0, 0, false
	}
	var tot, avail uint64
	var haveT, haveA bool
	for line := range bytes.Lines(b) {
		f := bytes.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, err := strconv.ParseUint(string(f[1]), 10, 64)
		if err != nil {
			continue
		}
		switch string(f[0]) {
		case "MemTotal:":
			tot, haveT = n*1024, true
		case "MemAvailable:":
			avail, haveA = n*1024, true
		}
	}
	if !haveT || !haveA || avail > tot {
		return 0, 0, false
	}
	return float64(tot - avail), float64(tot), true
}

// cpuCount counts the cpuN lines of /proc/stat.
func cpuCount(proc string) int {
	b, ok := readFile(filepath.Join(proc, "stat"))
	if !ok {
		return 1
	}
	n := 0
	for line := range bytes.Lines(b) {
		if len(line) > 3 && bytes.HasPrefix(line, []byte("cpu")) && line[3] >= '0' && line[3] <= '9' {
			n++
		}
	}
	return max(n, 1)
}

// bootTime is btime from /proc/stat, in unix seconds; 0 when unknown.
func bootTime(proc string) int64 {
	b, ok := readFile(filepath.Join(proc, "stat"))
	if !ok {
		return 0
	}
	v, _ := keyed(b, "btime")
	return int64(v)
}

// disks tells real disks from zram, loop and partitions by whether the
// block device has a backing device, and remembers the answer.
type disks struct {
	devBlock string
	known    map[string]bool
}

func (d *disks) isDisk(majMin string) bool {
	if v, ok := d.known[majMin]; ok {
		return v
	}
	_, err := os.Stat(filepath.Join(d.devBlock, majMin, "device"))
	// A partition has a device link too, through its disk; it is told apart
	// by its own "partition" file.
	_, perr := os.Stat(filepath.Join(d.devBlock, majMin, "partition"))
	v := err == nil && perr != nil
	if d.known == nil {
		d.known = map[string]bool{}
	}
	d.known[majMin] = v
	return v
}

// readIO sums rbytes and wbytes of io.stat over real disks.
func (d *disks) readIO(dir string) (r, w uint64) {
	b, ok := readFile(filepath.Join(dir, "io.stat"))
	if !ok {
		return 0, 0
	}
	for line := range bytes.Lines(b) {
		f := bytes.Fields(line)
		if len(f) < 2 || !d.isDisk(string(f[0])) {
			continue
		}
		for _, kv := range f[1:] {
			k, v, _ := bytes.Cut(kv, []byte("="))
			n, _ := strconv.ParseUint(string(v), 10, 64)
			switch string(k) {
			case "rbytes":
				r += n
			case "wbytes":
				w += n
			}
		}
	}
	return r, w
}

// hostNet sums /proc/net/dev over physical interfaces: those with a device
// behind them, so loopback, the stacks' veths, WireGuard and Tailscale are
// left out.
func hostNet(proc, sysNet string) (rx, tx uint64) {
	f, err := os.Open(filepath.Join(proc, "net", "dev"))
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if _, err := os.Stat(filepath.Join(sysNet, name, "device")); err != nil {
			continue
		}
		fs := strings.Fields(rest)
		if len(fs) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(fs[0], 10, 64)
		t, _ := strconv.ParseUint(fs[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx
}

// vethNet reads a stack's traffic from the host end of its veth. What the
// host end receives the stack sent, so the two are swapped.
func vethNet(sysNet, iface string) (rx, tx uint64, ok bool) {
	stats := filepath.Join(sysNet, iface, "statistics")
	hostRx, ok1 := readUint(filepath.Join(stats, "rx_bytes"))
	hostTx, ok2 := readUint(filepath.Join(stats, "tx_bytes"))
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	return hostTx, hostRx, true
}

// sensors are the temperature inputs found once at start.
type sensors struct {
	cpu, disk string // *_input paths in millidegrees; "" when absent
}

// findSensors picks coretemp's "Package id 0" (else its first input) and the
// NVMe drive's first input.
func findSensors(hwmon string) sensors {
	var s sensors
	dirs, _ := filepath.Glob(filepath.Join(hwmon, "hwmon*"))
	for _, d := range dirs {
		name, _ := readFile(filepath.Join(d, "name"))
		switch strings.TrimSpace(string(name)) {
		case "coretemp", "k10temp", "zenpower":
			s.cpu = filepath.Join(d, "temp1_input")
			labels, _ := filepath.Glob(filepath.Join(d, "temp*_label"))
			for _, l := range labels {
				if b, _ := readFile(l); strings.HasPrefix(strings.TrimSpace(string(b)), "Package id") || strings.TrimSpace(string(b)) == "Tctl" {
					s.cpu = strings.TrimSuffix(l, "_label") + "_input"
					break
				}
			}
		case "nvme", "drivetemp":
			if s.disk == "" {
				s.disk = filepath.Join(d, "temp1_input")
			}
		}
	}
	return s
}

// readTemp reads a millidegree input as °C, or NaN.
func readTemp(path string) float64 {
	if path == "" {
		return nan
	}
	v, ok := readUint(path)
	if !ok {
		return nan
	}
	return float64(v) / 1000
}
