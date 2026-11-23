package powerprofile

import (
	"bufio"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// cpuState is one CPU idle state summed over every CPU.
type cpuState struct {
	index   int
	name    string
	latency int
	us      uint64 // cumulative residency, µs
}

// pkgState is one package C-state's cumulative residency.
type pkgState struct {
	name string
	us   uint64
}

// sample is one reading of the idle counters.
type sample struct {
	at       time.Time
	uptime   float64 // seconds since boot, from /proc/uptime
	cpus     int
	states   []cpuState
	pkg      []pkgState
	energy   uint64 // µJ, summed over the packages
	maxRange uint64 // µJ, where the counter wraps
	rapl     bool
}

func (p Paths) sample(now time.Time) sample {
	s := sample{at: now}
	if v, ok := read(filepath.Join(p.Proc, "uptime")); ok {
		f, _, _ := strings.Cut(v, " ")
		s.uptime, _ = strconv.ParseFloat(f, 64)
	}
	byIndex := map[int]*cpuState{}
	for _, cpu := range glob(filepath.Join(p.CPU, "cpu[0-9]*")) {
		dirs := glob(filepath.Join(cpu, "cpuidle", "state[0-9]*"))
		if len(dirs) == 0 {
			continue
		}
		s.cpus++
		for _, d := range dirs {
			i, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(d), "state"))
			if err != nil {
				continue
			}
			st := byIndex[i]
			if st == nil {
				name, _ := read(filepath.Join(d, "name"))
				lat, _ := read(filepath.Join(d, "latency"))
				l, _ := strconv.Atoi(lat)
				st = &cpuState{index: i, name: name, latency: l}
				byIndex[i] = st
			}
			if v, ok := read(filepath.Join(d, "time")); ok {
				n, _ := strconv.ParseUint(v, 10, 64)
				st.us += n
			}
		}
	}
	for _, st := range byIndex {
		s.states = append(s.states, *st)
	}
	slices.SortFunc(s.states, func(a, b cpuState) int { return a.index - b.index })

	if v, ok := read(filepath.Join(p.PMC, "package_cstate_show")); ok {
		sc := bufio.NewScanner(strings.NewReader(v))
		for sc.Scan() {
			// "Package C3 : 723188955", in µs.
			k, n, ok := strings.Cut(sc.Text(), ":")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			if !strings.HasPrefix(k, "Package C") {
				continue
			}
			us, err := strconv.ParseUint(strings.TrimSpace(n), 10, 64)
			if err != nil {
				continue
			}
			s.pkg = append(s.pkg, pkgState{name: "PC" + strings.TrimPrefix(k, "Package C"), us: us})
		}
	}

	// The package zones are intel-rapl:N (AMD's rapl driver uses the same
	// name); intel-rapl:N:M are their parts.
	for _, z := range glob(filepath.Join(p.Powercap, "intel-rapl:*")) {
		if strings.Count(filepath.Base(z), ":") != 1 {
			continue
		}
		e, ok := read(filepath.Join(z, "energy_uj"))
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(e, 10, 64)
		if err != nil {
			continue
		}
		m, _ := read(filepath.Join(z, "max_energy_range_uj"))
		mr, _ := strconv.ParseUint(m, 10, 64)
		s.energy += n
		s.maxRange += mr
		s.rapl = true
	}
	return s
}

// idle is where the CPU spent the time between two samples. With no earlier
// sample (prev.at zero) the window is the time since boot, and there are no
// watts.
func idle(prev, now sample) PowerIdle {
	out := PowerIdle{CPUStates: []PowerIdleState{}, PackageStates: []PowerIdleState{}}
	window := now.uptime - prev.uptime
	if !prev.at.IsZero() && prev.uptime == 0 {
		window = now.at.Sub(prev.at).Seconds()
	}
	if window <= 0 {
		return out
	}
	out.WindowSeconds = window
	before := map[int]uint64{}
	for _, st := range prev.states {
		before[st.index] = st.us
	}
	idleShare := 0.0
	if now.cpus > 0 {
		for _, st := range now.states {
			d := float64(sub(st.us, before[st.index])) / 1e6 / (window * float64(now.cpus))
			d = clamp(d)
			idleShare += d
			out.CPUStates = append(out.CPUStates, PowerIdleState{Name: st.name, LatencyUS: st.latency, Share: d})
		}
		out.Active = clamp(1 - idleShare)
	}
	pkgBefore := map[string]uint64{}
	for _, st := range prev.pkg {
		pkgBefore[st.name] = st.us
	}
	for _, st := range now.pkg {
		d := clamp(float64(sub(st.us, pkgBefore[st.name])) / 1e6 / window)
		out.PackageStates = append(out.PackageStates, PowerIdleState{Name: st.name, Share: d})
		if d > 0 {
			out.Deepest = st.name
		}
	}
	if secs := now.at.Sub(prev.at).Seconds(); !prev.at.IsZero() && secs > 0 && prev.rapl && now.rapl {
		e := now.energy - prev.energy
		if now.energy < prev.energy {
			e = now.maxRange - prev.energy + now.energy
		}
		w := float64(e) / 1e6 / secs
		out.Watts = &w
	}
	return out
}

func sub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

func clamp(v float64) float64 {
	return max(0, min(1, v))
}

// blockers are the devices that keep the package awake: a PCIe link with
// L1 off, a PCI device with runtime PM on, a USB device that never
// autosuspends.
func (p Paths) blockers() []PowerBlocker {
	out := []PowerBlocker{}
	for _, d := range p.pciDevices() {
		dev := PowerBlocker{Device: filepath.Base(d), ID: pciID(d), Driver: driverOf(d)}
		if v, ok := read(filepath.Join(d, "link", "l1_aspm")); ok && v == "0" {
			b := dev
			b.Kind, b.Fix = BlockerASPM, PowerSaver
			out = append(out, b)
		}
		if v, ok := read(filepath.Join(d, "power", "control")); ok && v == "on" {
			b := dev
			b.Kind, b.Fix = BlockerRuntimePM, Balanced
			out = append(out, b)
		}
	}
	for _, d := range p.usbDevices() {
		if v, ok := read(filepath.Join(d, "power", "control")); ok && v == "on" {
			name, _ := read(filepath.Join(d, "product"))
			drv := driverOf(d)
			if drv == "usb" { // the generic device driver says nothing
				drv = ""
			}
			out = append(out, PowerBlocker{
				Kind: BlockerUSB, Device: filepath.Base(d), ID: usbID(d), Driver: drv, Name: name, Fix: PowerSaver,
			})
		}
	}
	return out
}
