package powerprofile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// group is one tunable: the value the profile wants and the files it goes
// to, in the order they are written. An empty value writes back the boot
// value.
type group struct {
	name   string
	value  string
	paths  []string
	detail string // why a value is not written at all
	// allowed, when set, lists the values the kernel accepts; a value
	// outside it is refused without writing.
	allowed []string
}

// pstate drivers have their own powersave governor that scales the
// frequency (with EPP); on acpi-cpufreq, powersave pins the lowest.
var pstate = []string{"intel_pstate", "amd-pstate-epp"}

// governorFor is the governor the profile runs, or empty to leave the boot
// one: performance for Performance; otherwise the pstate driver's
// powersave, or schedutil.
func governorFor(prof PowerProfile, driver string, avail []string) string {
	has := func(g string) bool { return slices.Contains(avail, g) }
	if prof == Performance {
		if has("performance") {
			return "performance"
		}
		return ""
	}
	if slices.Contains(pstate, driver) && has("powersave") {
		return "powersave"
	}
	if has("schedutil") {
		return "schedutil"
	}
	return ""
}

var eppFor = map[PowerProfile]string{
	PowerSaver: "power", Balanced: "balance_performance", Performance: "performance",
}

// aspmLinkFiles are a link's L1 switches, L1 first: a substate needs L1.
var aspmLinkFiles = []string{"l1_aspm", "l1_1_aspm", "l1_2_aspm"}

// plan is every tunable the profile touches, in the order they are written.
// The ASPM policy goes before the links, since writing it reconfigures them.
func (p Paths) plan(s PowerProfileSettings) []group {
	prof := s.Profile
	var out []group
	add := func(g group) { out = append(out, g) }

	pols := p.policies()
	files := func(name string) []string {
		var o []string
		for _, d := range pols {
			o = append(o, filepath.Join(d, name))
		}
		return o
	}
	gov := governorFor(prof, p.driver(), p.governors())
	if s.Governor != "" {
		gov = s.Governor
	}
	add(group{name: "governor", value: gov, paths: files("scaling_governor")})

	epp := group{name: "epp", paths: files("energy_performance_preference")}
	effective := gov
	if effective == "" {
		effective = p.currentGovernor()
	}
	if effective == "powersave" {
		epp.value = eppFor[prof]
		for _, d := range pols {
			if v, ok := read(filepath.Join(d, "energy_performance_available_preferences")); ok {
				epp.allowed = strings.Fields(v)
				break
			}
		}
	} else if effective != "" {
		epp.detail = "set by the " + effective + " governor"
	}
	add(epp)

	pol := map[PowerProfile]string{PowerSaver: "powersupersave", Performance: "performance"}[prof]
	add(group{name: "aspm_policy", value: pol, paths: []string{p.ASPMPolicy}})

	links := group{name: "aspm_links"}
	order := aspmLinkFiles
	if prof == PowerSaver {
		links.value = "1"
	} else {
		// Back to boot: substates off before L1, the reverse of turning on.
		order = slices.Clone(aspmLinkFiles)
		slices.Reverse(order)
	}
	for _, d := range p.pciDevices() {
		for _, f := range order {
			links.paths = append(links.paths, filepath.Join(d, "link", f))
		}
	}
	add(links)

	rpm := group{name: "pci_runtime_pm"}
	if prof != Performance {
		rpm.value = "auto"
	}
	for _, d := range p.pciDevices() {
		rpm.paths = append(rpm.paths, filepath.Join(d, "power", "control"))
	}
	add(rpm)

	usb := group{name: "usb_autosuspend"}
	if prof == PowerSaver {
		usb.value = "auto"
	}
	for _, d := range p.usbDevices() {
		usb.paths = append(usb.paths, filepath.Join(d, "power", "control"))
	}
	add(usb)

	alpm := group{name: "sata_alpm", value: "med_power_with_dipm"}
	if prof == Performance {
		alpm.value = "max_performance"
	}
	alpm.paths = glob(filepath.Join(p.SCSIHost, "host*", "link_power_management_policy"))
	add(alpm)

	nmi := group{name: "nmi_watchdog", paths: []string{filepath.Join(p.ProcSys, "kernel", "nmi_watchdog")}}
	if prof != Performance {
		nmi.value = "0"
	}
	add(nmi)

	dw := group{name: "dirty_writeback", paths: []string{filepath.Join(p.ProcSys, "vm", "dirty_writeback_centisecs")}}
	if prof == PowerSaver {
		dw.value = "1500"
	}
	add(dw)
	return out
}

// currentGovernor is the first policy's governor; it only matters when a
// profile leaves the governor alone.
func (p Paths) currentGovernor() string {
	for _, d := range p.policies() {
		if v, ok := read(filepath.Join(d, "scaling_governor")); ok {
			return v
		}
	}
	return ""
}

// applier writes a plan, keeping the value each file had before its first
// write so a later profile can put it back.
type applier struct {
	paths Paths
	boot  map[string]string
	write func(path, value string) error
}

func newApplier(p Paths) *applier {
	a := &applier{paths: p, boot: map[string]string{}, write: writeFile}
	if b, err := os.ReadFile(p.Boot); err == nil {
		_ = json.Unmarshal(b, &a.boot)
	}
	return a
}

func writeFile(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// record keeps the current value of every file in the plan not seen
// before. It runs before any write, since one write (the ASPM policy)
// changes other files.
func (a *applier) record(groups []group) {
	added := false
	for _, g := range groups {
		for _, path := range g.paths {
			if _, ok := a.boot[path]; ok {
				continue
			}
			if v, ok := current(path); ok {
				a.boot[path] = v
				added = true
			}
		}
	}
	if added && a.paths.Boot != "" {
		if b, err := json.Marshal(a.boot); err == nil {
			_ = os.MkdirAll(filepath.Dir(a.paths.Boot), 0o755)
			_ = os.WriteFile(a.paths.Boot, b, 0o600)
		}
	}
}

// apply writes the plan and reports each tunable. Only files whose value
// differs are written, so a sweep over an applied host writes nothing.
func (a *applier) apply(groups []group) []PowerTunable {
	a.record(groups)
	out := make([]PowerTunable, 0, len(groups))
	for _, g := range groups {
		t := PowerTunable{Name: g.name, Value: g.value, Detail: g.detail}
		for _, path := range g.paths {
			cur, ok := current(path)
			if !ok {
				continue
			}
			t.Files++
			want := g.value
			if want == "" {
				want = a.boot[path]
			}
			if want == "" || cur == want {
				continue
			}
			if g.allowed != nil && !slices.Contains(g.allowed, want) {
				t.Refused++
				if t.Detail == "" {
					t.Detail = want + " is not offered"
				}
				continue
			}
			if err := a.write(path, want); err != nil {
				t.Refused++
				if t.Detail == "" {
					t.Detail = describe(g.name, err)
				}
			}
		}
		switch {
		case t.Files == 0:
			t.State = StateUnsupported
		case t.Refused == t.Files:
			t.State = StateRefused
		case t.Refused > 0:
			t.State = StatePartial
		case g.value == "":
			t.State = StateBoot
		default:
			t.State = StateSet
		}
		out = append(out, t)
	}
	return out
}

// describe says why the kernel refused a write.
func describe(name string, err error) string {
	switch {
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		if name == "aspm_policy" || name == "aspm_links" {
			return "not allowed by firmware"
		}
		return "not allowed"
	case errors.Is(err, syscall.EINVAL), errors.Is(err, syscall.EOPNOTSUPP), errors.Is(err, syscall.ENODEV):
		return "not supported by the device"
	case errors.Is(err, syscall.EBUSY):
		return "busy"
	}
	return err.Error()
}
