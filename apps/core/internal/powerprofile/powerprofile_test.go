package powerprofile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/store/storetest"
)

// fakeHost is a sysfs and /proc tree in a temp dir.
type fakeHost struct {
	t     *testing.T
	root  string
	paths Paths
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	root := t.TempDir()
	j := func(s ...string) string { return filepath.Join(append([]string{root}, s...)...) }
	return &fakeHost{t: t, root: root, paths: Paths{
		CPU: j("cpu"), PCI: j("pci"), USB: j("usb"), SCSIHost: j("scsi_host"),
		ASPMPolicy: j("pcie_aspm", "policy"), ProcSys: j("proc", "sys"), Proc: j("proc"),
		PMC: j("pmc_core"), Powercap: j("powercap"), Boot: j("run", "boot.json"),
	}}
}

func (h *fakeHost) write(rel, body string) {
	h.t.Helper()
	p := filepath.Join(h.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body+"\n"), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *fakeHost) get(rel string) string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.root, rel))
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// intel is the live-host shape: intel_pstate, a NIC with ASPM off, a USB
// device with autosuspend off, ACPI granting ASPM.
func (h *fakeHost) intel() {
	for i := range 2 {
		d := fmt.Sprintf("cpu/cpufreq/policy%d/", i)
		h.write(d+"scaling_driver", "intel_pstate")
		h.write(d+"scaling_available_governors", "performance powersave")
		h.write(d+"scaling_governor", "powersave")
		h.write(d+"energy_performance_preference", "balance_performance")
		h.write(d+"energy_performance_available_preferences", "default performance balance_performance balance_power power")
	}
	h.write("pcie_aspm/policy", "[default] performance powersave powersupersave")
	h.write("pci/0000:03:00.0/vendor", "0x10ec")
	h.write("pci/0000:03:00.0/device", "0x8125")
	for _, f := range aspmLinkFiles {
		h.write("pci/0000:03:00.0/link/"+f, "0")
	}
	h.write("pci/0000:03:00.0/power/control", "on")
	h.write("pci/0000:00:02.0/power/control", "auto")
	h.write("usb/1-6/power/control", "on")
	h.write("usb/1-6/idVendor", "0b05")
	h.write("usb/1-6/idProduct", "19af")
	h.write("usb/1-6/product", "AURA LED Controller")
	h.write("usb/1-6:1.0/bInterfaceClass", "03")
	h.write("scsi_host/host0/link_power_management_policy", "med_power_with_dipm")
	h.write("proc/sys/kernel/nmi_watchdog", "1")
	h.write("proc/sys/vm/dirty_writeback_centisecs", "500")
}

func (h *fakeHost) tunables(ts []PowerTunable) map[string]PowerTunable {
	m := map[string]PowerTunable{}
	for _, t := range ts {
		m[t.Name] = t
	}
	return m
}

// fakeWrite writes like the kernel does for a choice file: the chosen value
// is bracketed among the options.
func fakeWrite(path, value string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(string(b)); strings.Contains(s, "[") {
		opts := strings.Fields(strings.NewReplacer("[", "", "]", "").Replace(s))
		for i, o := range opts {
			if o == value {
				opts[i] = "[" + o + "]"
			}
		}
		value = strings.Join(opts, " ")
	}
	return os.WriteFile(path, []byte(value+"\n"), 0o644)
}

func (h *fakeHost) applier() *applier {
	a := newApplier(h.paths)
	a.write = fakeWrite
	return a
}

func TestPowerSaverThenBalancedRestoresBoot(t *testing.T) {
	h := newFakeHost(t)
	h.intel()
	a := h.applier()

	got := h.tunables(a.apply(h.paths.plan(PowerProfileSettings{Profile: PowerSaver})))
	want := map[string]string{
		"cpu/cpufreq/policy0/scaling_governor":              "powersave",
		"cpu/cpufreq/policy1/energy_performance_preference": "power",
		"pcie_aspm/policy":                                  "default performance powersave [powersupersave]",
		"pci/0000:03:00.0/link/l1_aspm":                     "1",
		"pci/0000:03:00.0/link/l1_2_aspm":                   "1",
		"pci/0000:03:00.0/power/control":                    "auto",
		"usb/1-6/power/control":                             "auto",
		"proc/sys/kernel/nmi_watchdog":                      "0",
		"proc/sys/vm/dirty_writeback_centisecs":             "1500",
		"scsi_host/host0/link_power_management_policy":      "med_power_with_dipm",
	}
	for rel, v := range want {
		if g := h.get(rel); g != v {
			t.Errorf("power saver: %s = %q, want %q", rel, g, v)
		}
	}
	if got["aspm_links"].State != StateSet || got["aspm_links"].Files != 3 || got["usb_autosuspend"].Files != 1 {
		t.Errorf("report: %+v", got)
	}

	// A restarted daemon reads the boot values back from /run.
	b := h.applier()
	got = h.tunables(b.apply(h.paths.plan(PowerProfileSettings{Profile: Balanced})))
	want = map[string]string{
		"cpu/cpufreq/policy0/energy_performance_preference": "balance_performance",
		"pcie_aspm/policy":                      "[default] performance powersave powersupersave",
		"pci/0000:03:00.0/link/l1_aspm":         "0",
		"pci/0000:03:00.0/link/l1_1_aspm":       "0",
		"pci/0000:03:00.0/power/control":        "auto",
		"usb/1-6/power/control":                 "on",
		"proc/sys/kernel/nmi_watchdog":          "0",
		"proc/sys/vm/dirty_writeback_centisecs": "500",
	}
	for rel, v := range want {
		if g := h.get(rel); g != v {
			t.Errorf("balanced: %s = %q, want %q", rel, g, v)
		}
	}
	if got["aspm_links"].State != StateBoot || got["pci_runtime_pm"].State != StateSet {
		t.Errorf("report: %+v", got)
	}
}

func TestPerformanceSkipsEPP(t *testing.T) {
	h := newFakeHost(t)
	h.intel()
	a := h.applier()
	var wrote []string
	a.write = func(path, value string) error {
		wrote = append(wrote, filepath.Base(path))
		if filepath.Base(path) == "energy_performance_preference" {
			return syscall.EBUSY
		}
		return fakeWrite(path, value)
	}
	got := h.tunables(a.apply(h.paths.plan(PowerProfileSettings{Profile: Performance})))
	if h.get("cpu/cpufreq/policy0/scaling_governor") != "performance" {
		t.Error("governor not performance")
	}
	for _, w := range wrote {
		if w == "energy_performance_preference" {
			t.Fatal("EPP written under the performance governor")
		}
	}
	if e := got["epp"]; e.State != StateBoot || e.Detail != "set by the performance governor" {
		t.Errorf("epp: %+v", e)
	}
	if h.get("scsi_host/host0/link_power_management_policy") != "max_performance" {
		t.Error("ALPM not max_performance")
	}
	if h.get("pci/0000:03:00.0/power/control") != "on" {
		t.Error("runtime PM changed under performance")
	}
}

func TestGovernorOverride(t *testing.T) {
	h := newFakeHost(t)
	h.intel()
	a := h.applier()
	a.apply(h.paths.plan(PowerProfileSettings{Profile: PowerSaver, Governor: "performance"}))
	if h.get("cpu/cpufreq/policy1/scaling_governor") != "performance" {
		t.Error("override ignored")
	}
	if h.get("cpu/cpufreq/policy1/energy_performance_preference") != "balance_performance" {
		t.Error("EPP written under an overridden performance governor")
	}
}

func TestACPICpufreqUsesSchedutil(t *testing.T) {
	h := newFakeHost(t)
	h.write("cpu/cpufreq/policy0/scaling_driver", "acpi-cpufreq")
	h.write("cpu/cpufreq/policy0/scaling_available_governors", "conservative ondemand userspace powersave performance schedutil")
	h.write("cpu/cpufreq/policy0/scaling_governor", "ondemand")
	a := h.applier()
	got := h.tunables(a.apply(h.paths.plan(PowerProfileSettings{Profile: PowerSaver})))
	if h.get("cpu/cpufreq/policy0/scaling_governor") != "schedutil" {
		t.Error("power saver on acpi-cpufreq is not schedutil")
	}
	if got["epp"].State != StateUnsupported || got["aspm_policy"].State != StateUnsupported {
		t.Errorf("report: %+v", got)
	}
}

func TestRefusedPolicyStillAppliesTheRest(t *testing.T) {
	h := newFakeHost(t)
	h.intel()
	a := h.applier()
	a.write = func(path, value string) error {
		if path == h.paths.ASPMPolicy {
			return &os.PathError{Op: "write", Path: path, Err: syscall.EPERM}
		}
		return fakeWrite(path, value)
	}
	got := h.tunables(a.apply(h.paths.plan(PowerProfileSettings{Profile: PowerSaver})))
	if p := got["aspm_policy"]; p.State != StateRefused || p.Detail != "not allowed by firmware" {
		t.Errorf("policy: %+v", p)
	}
	if h.get("pci/0000:03:00.0/link/l1_aspm") != "1" || h.get("usb/1-6/power/control") != "auto" {
		t.Error("the rest was not applied")
	}
}

func TestSweepWritesNothingWhenApplied(t *testing.T) {
	h := newFakeHost(t)
	h.intel()
	a := h.applier()
	plan := PowerProfileSettings{Profile: PowerSaver}
	a.apply(h.paths.plan(plan))
	n := 0
	a.write = func(path, value string) error { n++; return fakeWrite(path, value) }
	a.apply(h.paths.plan(plan))
	if n != 0 {
		t.Errorf("sweep wrote %d files", n)
	}
	// A device plugged in since is caught.
	h.write("usb/1-9/power/control", "on")
	a.apply(h.paths.plan(plan))
	if n != 1 || h.get("usb/1-9/power/control") != "auto" {
		t.Errorf("new device: %d writes, %s", n, h.get("usb/1-9/power/control"))
	}
}

func TestIdleFromTwoSamples(t *testing.T) {
	h := newFakeHost(t)
	cpu := func(c int, s int, name string, lat int, us uint64) {
		d := fmt.Sprintf("cpu/cpu%d/cpuidle/state%d/", c, s)
		h.write(d+"name", name)
		h.write(d+"latency", fmt.Sprint(lat))
		h.write(d+"time", fmt.Sprint(us))
	}
	set := func(uptime float64, c1, c6 uint64, pc3, pc6 uint64, energy uint64) {
		h.write("proc/uptime", fmt.Sprintf("%.2f 0.00", uptime))
		for c := range 2 {
			cpu(c, 0, "POLL", 0, 0)
			cpu(c, 1, "C1", 1, c1)
			cpu(c, 2, "C6", 170, c6)
		}
		h.write("pmc_core/package_cstate_show", fmt.Sprintf("Package C2 : 0\nPackage C3 : %d\nPackage C6 : %d\n", pc3, pc6))
		h.write("powercap/intel-rapl:0/energy_uj", fmt.Sprint(energy))
		h.write("powercap/intel-rapl:0/max_energy_range_uj", "262143328850")
		h.write("powercap/intel-rapl:0:0/energy_uj", "999999999")
	}
	t0 := time.Unix(1000, 0)
	set(100, 0, 0, 0, 0, 262143328850-1_000_000)
	a := h.paths.sample(t0)
	// 60 s on 2 CPUs: each 6 s in C1 and 48 s in C6; the package 30 s in
	// PC3 and 15 s in PC6; 60 J, across the counter's wrap.
	set(160, 6e6, 48e6, 30e6, 15e6, 59_000_000)
	b := h.paths.sample(t0.Add(time.Minute))
	got := idle(a, b)
	if got.WindowSeconds != 60 || len(got.CPUStates) != 3 || got.CPUStates[2].Name != "C6" || got.CPUStates[2].LatencyUS != 170 {
		t.Fatalf("idle: %+v", got)
	}
	if d := got.CPUStates[2].Share - 0.8; d > 1e-9 || d < -1e-9 {
		t.Errorf("C6 share %v", got.CPUStates[2].Share)
	}
	if d := got.Active - 0.1; d > 1e-9 || d < -1e-9 {
		t.Errorf("active %v", got.Active)
	}
	if got.Deepest != "PC6" || got.PackageStates[1].Share != 0.5 {
		t.Errorf("package: %+v %s", got.PackageStates, got.Deepest)
	}
	if got.Watts == nil || *got.Watts != 1 {
		t.Errorf("watts %v", got.Watts)
	}
	// Without an earlier sample: since boot, no watts.
	if s := idle(sample{}, b); s.WindowSeconds != 160 || s.Watts != nil {
		t.Errorf("since boot: %+v", s)
	}
}

func TestBlockers(t *testing.T) {
	h := newFakeHost(t)
	h.intel()
	got := h.paths.blockers()
	if len(got) != 3 {
		t.Fatalf("blockers: %+v", got)
	}
	if got[0].Kind != BlockerASPM || got[0].ID != "10ec:8125" || got[0].Fix != PowerSaver {
		t.Errorf("aspm: %+v", got[0])
	}
	if got[1].Kind != BlockerRuntimePM || got[1].Fix != Balanced {
		t.Errorf("runtime pm: %+v", got[1])
	}
	if got[2].Kind != BlockerUSB || got[2].Name != "AURA LED Controller" || got[2].ID != "0b05:19af" {
		t.Errorf("usb: %+v", got[2])
	}
}

func TestServiceSet(t *testing.T) {
	h := newFakeHost(t)
	h.intel()
	st, _ := storetest.Open(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	ro := New(h.paths, st, false, log)
	if _, err := ro.Set(ctx, PowerProfileSettings{Profile: PowerSaver}); !errors.Is(err, ErrManaged) {
		t.Errorf("NixOS host: %v", err)
	}

	s := New(h.paths, st, true, log)
	s.ap.write = fakeWrite
	for _, bad := range []PowerProfileSettings{{Profile: "turbo"}, {Profile: Balanced, Governor: "ondemand"}} {
		if _, err := s.Set(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	got, err := s.Set(ctx, PowerProfileSettings{Profile: PowerSaver})
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != PowerSaver || got.Driver != "intel_pstate" || len(got.Tunables) != 9 || len(got.Blockers) != 0 {
		t.Errorf("status: %+v", got)
	}

	// A new start reads the saved profile.
	again := New(h.paths, st, true, log)
	if st, err := again.Status(ctx); err != nil || st.Profile != PowerSaver {
		t.Errorf("reloaded: %+v %v", st.PowerProfileSettings, err)
	}
}
