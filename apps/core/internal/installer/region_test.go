package installer

import (
	"strconv"
	"strings"
	"testing"
)

func TestReservations(t *testing.T) {
	d := assess(windows(512 * mib))
	if len(d.Free) != 2 {
		t.Fatalf("free runs %+v", d.Free)
	}
	// Picking the large run holds the gap before Windows, and the other
	// way round.
	got := reservations(d, d.Free[1].Start, 512)
	want := []string{"start=" + itoa(d.Free[0].Start/512) + ", size=" + itoa(d.Free[0].Size/512) + ", type=" + reservedType + ", name=" + reservedName}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := reservations(d, d.Free[0].Start, 4096); len(got) != 1 || !strings.Contains(got[0], "start="+itoa(d.Free[1].Start/4096)+",") {
		t.Fatalf("got %q", got)
	}
	if !regionOK(d, d.Free[1].Start) || regionOK(d, d.Free[1].Start+mib) {
		t.Fatal("regionOK")
	}
}

func TestPartPath(t *testing.T) {
	for in, want := range map[string]string{"/dev/nvme0n1": "/dev/nvme0n1p3", "/dev/sda": "/dev/sda3", "/dev/vdc": "/dev/vdc3"} {
		if got := partPath(in, 3); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestCopyTableAndPlanOnDisk(t *testing.T) {
	dump := `label: gpt
label-id: 1377CD1D-5A28-49EB-877C-E833D108B6A4
device: /dev/nvme0n1
unit: sectors
first-lba: 34
last-lba: 4194270
sector-size: 512

/dev/nvme0n1p1 : start=        2048, size=      204800, type=C12A7328-F81F-11D2-BA4B-00A0C93EC93B, name="EFI"
/dev/nvme0n1p3 : start=      206848, size=     1000000, type=EBD0A0A2-B9E5-4433-87C0-68B6B72699C7`
	got := copyTable(dump, "/dev/nvme0n1", "/run/x/disk")
	if strings.Contains(got, "device:") || !strings.Contains(got, "\n/run/x/disk1 : start=        2048,") || !strings.Contains(got, "\n/run/x/disk3 : start=") {
		t.Fatalf("copy:\n%s", got)
	}
	p := planOnDisk(InstallPlan{OK: true, Rows: []InstallPlanRow{
		{Node: "/run/x/disk1", Label: "EFI"},
		{Node: "/run/x/disk2", Label: reservedName},
		{Node: "/run/x/disk4", Label: "root-x86-64"},
	}}, "/run/x/disk", "/dev/nvme0n1")
	if len(p.Rows) != 2 || p.Rows[0].Node != "/dev/nvme0n1p1" || p.Rows[1].Node != "/dev/nvme0n1p4" {
		t.Fatalf("plan %+v", p.Rows)
	}
}

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }
