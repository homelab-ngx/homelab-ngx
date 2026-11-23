package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMachine(t *testing.T) {
	dir := t.TempDir()
	put := func(name, s string) { _ = os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644) }
	put("sys_vendor", "To Be Filled By O.E.M.\n")
	put("product_name", "To Be Filled By O.E.M.\n")
	put("board_vendor", "ASRock\n")
	put("board_name", "B550M Pro4\n")
	if m := machineModel(dir); m != "ASRock B550M Pro4" {
		t.Errorf("placeholder product: %q", m)
	}
	put("sys_vendor", "LENOVO\n")
	put("product_name", "Lenovo ThinkCentre M720q\n")
	if m := machineModel(dir); m != "Lenovo ThinkCentre M720q" {
		t.Errorf("vendor in the name: %q", m)
	}
	put("sys_vendor", "Intel Corporation\n")
	put("product_name", "NUC8i5BEH\n")
	if m := machineModel(dir); m != "Intel Corporation NUC8i5BEH" {
		t.Errorf("model = %q", m)
	}
	if m := machineModel(filepath.Join(dir, "none")); m != "" {
		t.Errorf("no DMI: %q", m)
	}

	put("cpuinfo", "processor\t: 0\nvendor_id\t: AuthenticAMD\nmodel name\t: AMD Ryzen 5 5600G  with Radeon Graphics\n\nprocessor\t: 1\n")
	if c := cpuName(dir); c != "AMD Ryzen 5 5600G with Radeon Graphics" {
		t.Errorf("cpu = %q", c)
	}
}
