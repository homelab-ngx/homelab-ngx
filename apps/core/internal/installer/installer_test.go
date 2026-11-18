package installer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/godbus/dbus/v5"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/hostcfg"
)

func part(n int, name string, start, size uint64, typ, fs string) InstallPartition {
	return InstallPartition{Number: n, Name: name, Path: "/dev/" + name, Start: start, Size: size, Type: typ, FS: fs, Holds: holds(typ, fs, "")}
}

// windows is a 256 GB disk as Windows leaves it: ESP, MSR, C: and the
// recovery partition, with 100 GB shrunk off C: at the end.
func windows(espSize uint64) InstallDisk {
	return InstallDisk{Name: "nvme0n1", Path: "/dev/nvme0n1", Size: 256 * gib, Table: "gpt", Partitions: []InstallPartition{
		part(1, "nvme0n1p1", mib, espSize, typeESP, "vfat"),
		part(2, "nvme0n1p2", mib+espSize, 16*mib, typeMSR, ""),
		part(3, "nvme0n1p3", mib+espSize+16*mib, 150*gib, typeMSData, "ntfs"),
		part(4, "nvme0n1p4", 152*gib, gib, typeWinRE, "ntfs"),
	}}
}

func TestAssess(t *testing.T) {
	for _, c := range []struct {
		name          string
		disk          InstallDisk
		erase, beside bool
	}{
		{"windows with 100 GB free", windows(512 * mib), true, true},
		{"windows with its 100 MB ESP", windows(100 * mib), true, false},
		{"blank", InstallDisk{Name: "sda", Size: 500 * gib, Table: "none"}, true, false},
		{"too small", InstallDisk{Name: "sda", Size: 32 * gib, Table: "none"}, false, false},
		{"the stick", InstallDisk{Name: "sdb", Size: 128 * gib, Table: "gpt", InUse: true,
			Partitions: []InstallPartition{part(1, "sdb1", mib, gib, typeESP, "vfat")}}, false, false},
		{"another discoverable linux", InstallDisk{Name: "sda", Size: 500 * gib, Table: "gpt",
			Partitions: []InstallPartition{part(1, "sda1", mib, 20*gib, typeRootX64, "ext4")}}, true, false},
		{"mbr", InstallDisk{Name: "sda", Size: 500 * gib, Table: "dos",
			Partitions: []InstallPartition{part(1, "sda1", mib, 20*gib, "0x07", "ntfs")}}, true, false},
		{"full", InstallDisk{Name: "sda", Size: 100 * gib, Table: "gpt",
			Partitions: []InstallPartition{part(1, "sda1", mib, 90*gib, "0FC63DAF-8483-4772-8E79-3D69D8477DE4", "ext4")}}, true, false},
	} {
		d := assess(c.disk)
		if d.Erase.OK != c.erase || d.Beside.OK != c.beside {
			t.Errorf("%s: erase %+v beside %+v", c.name, d.Erase, d.Beside)
		}
		if !d.Erase.OK && d.Erase.Reason == "" || !d.Beside.OK && d.Beside.Reason == "" {
			t.Errorf("%s: a refusal without a reason", c.name)
		}
	}
}

func TestFreeRuns(t *testing.T) {
	d := assess(windows(512 * mib))
	if len(d.Free) != 2 {
		t.Fatalf("free = %+v", d.Free)
	}
	// The gap between C: and the recovery partition, and the end of the
	// disk less the backup table.
	if d.Free[1].Start != 153*gib || d.Free[1].Size != 103*gib-mib {
		t.Errorf("last run = %+v", d.Free[1])
	}
}

func TestHolds(t *testing.T) {
	for _, c := range []struct{ typ, fs, label, want string }{
		{typeESP, "vfat", "", "esp"},
		{typeMSData, "BitLocker", "", "windows"},
		{typeMSData, "", "", "windows"},
		{typeMSR, "", "", "windows"},
		{"0FC63DAF-8483-4772-8E79-3D69D8477DE4", "btrfs", "", "linux"},
		{"0FC63DAF-8483-4772-8E79-3D69D8477DE4", "", "", "empty"},
		{typeRootX64, "erofs", "homelab_0.0.1", "homelab"},
		{typeNix, "ext4", "nix", "homelab"},
		{"0FC63DAF-8483-4772-8E79-3D69D8477DE4", "zfs_member", "", "other"},
	} {
		if got := holds(c.typ, c.fs, c.label); got != c.want {
			t.Errorf("holds(%s, %s, %s) = %s, want %s", c.typ, c.fs, c.label, got, c.want)
		}
	}
}

func TestFromObjects(t *testing.T) {
	v := dbus.MakeVariant
	dev := func(s string) dbus.Variant { return v(append([]byte(s), 0)) }
	objs := objects{
		"/drives/ssd": {ifDrive: {"Vendor": v("Samsung"), "Model": v("990 EVO"), "ConnectionBus": v(""), "Removable": v(false)}},
		"/drives/usb": {ifDrive: {"Vendor": v(""), "Model": v("Flash"), "ConnectionBus": v("usb"), "Removable": v(true)}},
		"/block/nvme0n1": {ifBlock: {"Device": dev("/dev/nvme0n1"), "Size": v(uint64(256 * gib)), "Drive": v(dbus.ObjectPath("/drives/ssd"))},
			ifTable: {"Type": v("gpt")}},
		"/block/nvme0n1p1": {ifBlock: {"Device": dev("/dev/nvme0n1p1"), "IdType": v("vfat"), "Drive": v(dbus.ObjectPath("/drives/ssd"))},
			ifPart: {"Number": v(uint32(1)), "Type": v("c12a7328-f81f-11d2-ba4b-00a0c93ec93b"), "Offset": v(uint64(mib)), "Size": v(uint64(512 * mib)), "Name": v("EFI system partition"), "Table": v(dbus.ObjectPath("/block/nvme0n1"))},
			ifFS:   {"MountPoints": v([][]byte{})}},
		"/block/sda": {ifBlock: {"Device": dev("/dev/sda"), "Size": v(uint64(16 * gib)), "Drive": v(dbus.ObjectPath("/drives/usb"))},
			ifTable: {"Type": v("gpt")}},
		"/block/sda7": {ifBlock: {"Device": dev("/dev/sda7"), "IdType": v("ext4"), "Drive": v(dbus.ObjectPath("/drives/usb"))},
			ifPart: {"Number": v(uint32(7)), "Type": v("4d21b016-b534-45c2-a9fb-5c16e091fd2d"), "Offset": v(uint64(10 * gib)), "Size": v(uint64(gib)), "Name": v("var"), "Table": v(dbus.ObjectPath("/block/sda"))},
			ifFS:   {"MountPoints": v([][]byte{append([]byte("/var"), 0)})}},
		"/block/loop0": {ifBlock: {"Device": dev("/dev/loop0"), "Size": v(uint64(gib)), "Drive": v(dbus.ObjectPath("/"))}},
	}
	disks := fromObjects(objs)
	if len(disks) != 2 || disks[0].Name != "nvme0n1" || disks[1].Name != "sda" {
		t.Fatalf("disks = %+v", disks)
	}
	ssd, usb := disks[0], disks[1]
	if ssd.Model != "Samsung 990 EVO" || ssd.InUse || len(ssd.Partitions) != 1 || ssd.Partitions[0].Holds != "esp" || ssd.Partitions[0].Label != "EFI system partition" {
		t.Errorf("ssd = %+v", ssd)
	}
	if !usb.InUse || !usb.Removable || usb.Bus != "usb" || usb.Erase.OK {
		t.Errorf("the stick = %+v", usb)
	}
}

func TestParsePlan(t *testing.T) {
	out := `Refusing to repartition, please re-run with --dry-run=no.
[{"type":"esp","label":"esp","node":"/dev/vdc1","offset":1048576,"raw_size":536870912,"activity":"unchanged"},` +
		`{"type":"root-x86-64","label":"homelab_0.0.1","node":"/dev/vdc3","raw_size":4294967296,"activity":"create"}]`
	p := parsePlan(out, out)
	if !p.OK || len(p.Rows) != 2 || p.Rows[1].Size != 4*gib || p.Rows[1].Activity != "create" || p.Rows[0].Offset != mib {
		t.Fatalf("plan = %+v", p)
	}
	bad := "Automatically determined minimal disk image size as 45.1G.\nCan't fit requested partitions into available free space (20.0G), refusing.\n"
	if p := parsePlan("", bad); p.OK || p.Reason != "Can't fit requested partitions into available free space (20.0G), refusing." {
		t.Fatalf("plan = %+v", p)
	}
}

// writeFiles makes files under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for f, c := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFinishESP(t *testing.T) {
	// A shared ESP after bootctl install: systemd-boot's fallback replaced
	// Windows', and loader.conf has bootctl's lines.
	dir, stick := t.TempDir(), t.TempDir()
	writeFiles(t, dir, map[string]string{
		"EFI/BOOT/BOOTX64.EFI":            "SYSTEMD-BOOT",
		"EFI/Microsoft/Boot/bootmgfw.efi": "WINDOWS",
		"loader/loader.conf":              "#timeout 3\ndefault homelab-*\nconsole-mode keep\n",
	})
	writeFiles(t, stick, map[string]string{"EFI/Linux/homelab_0.0.1+2-1.efi": "UKI"})
	if err := finishESP(dir, filepath.Join(stick, "EFI/Linux/homelab_0.0.1+2-1.efi"), "0.0.1", []byte("WINDOWS-FALLBACK")); err != nil {
		t.Fatal(err)
	}
	read := func(f string) string { b, _ := os.ReadFile(filepath.Join(dir, f)); return string(b) }
	if read("EFI/Linux/homelab_0.0.1.efi") != "UKI" {
		t.Error("the kernel is not in EFI/Linux under its version, without a boot counter")
	}
	if got := read("loader/loader.conf"); got != "#timeout 3\nconsole-mode keep\ntimeout 3\neditor no\ndefault homelab_*\n" {
		t.Errorf("loader.conf = %q", got)
	}
	if read("EFI/BOOT/BOOTX64.EFI") != "WINDOWS-FALLBACK" || read("EFI/Microsoft/Boot/bootmgfw.efi") != "WINDOWS" {
		t.Error("another system's loaders changed")
	}

	// A blank disk had no fallback loader: systemd-boot's stays, and a
	// missing loader.conf is written.
	dir = t.TempDir()
	writeFiles(t, dir, map[string]string{"EFI/BOOT/BOOTX64.EFI": "SYSTEMD-BOOT"})
	if err := finishESP(dir, filepath.Join(stick, "EFI/Linux/homelab_0.0.1+2-1.efi"), "0.0.1", nil); err != nil {
		t.Fatal(err)
	}
	if read("EFI/BOOT/BOOTX64.EFI") != "SYSTEMD-BOOT" || read("loader/loader.conf") != "timeout 3\neditor no\ndefault homelab_*\n" {
		t.Errorf("blank disk: fallback %q, loader.conf %q", read("EFI/BOOT/BOOTX64.EFI"), read("loader/loader.conf"))
	}
}

func TestSeedVar(t *testing.T) {
	dir := t.TempDir()
	if err := seedVar(dir, Setup{Hostname: "box"}); err == nil {
		t.Fatal("a /var without the core extension passed")
	}
	writeFiles(t, dir, map[string]string{"lib/extensions/homelab-core.raw": "EXT"})
	_ = os.Chmod(filepath.Join(dir, "lib"), 0o750) // as a umask of 0027 leaves it
	ip := hostcfg.IPv4{Mode: "static", Address: "192.168.1.20/24", Gateway: "192.168.1.1"}
	if err := seedVar(dir, Setup{Hostname: "box", Timezone: "Europe/Stockholm", IPv4: ip}); err != nil {
		t.Fatal(err)
	}
	want := `{"hostname":"box","timezone":"Europe/Stockholm","ipv4":{"mode":"static","address":"192.168.1.20/24","gateway":"192.168.1.1"}}` + "\n"
	if b, _ := os.ReadFile(filepath.Join(dir, "lib/homelab/install.json")); string(b) != want {
		t.Errorf("install.json = %q", b)
	}
	for _, sub := range []string{"lib", "lib/homelab"} {
		if fi, _ := os.Stat(filepath.Join(dir, sub)); fi.Mode().Perm() != 0o755 {
			t.Errorf("%s is %v: other system users cannot enter it", sub, fi.Mode().Perm())
		}
	}
}

func TestImageVersion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "os-release")
	_ = os.WriteFile(p, []byte("ID=arch\nIMAGE_ID=homelab\nIMAGE_VERSION=\"0.0.1\"\n"), 0o644)
	if v := imageVersion(p); v != "0.0.1" {
		t.Errorf("version = %q", v)
	}
}

type fakeDisks struct{ list []InstallDisk }

func (f fakeDisks) List(context.Context) ([]InstallDisk, error) { return f.list, nil }
func (fakeDisks) DeletePartition(context.Context, string) error { return nil }

func TestClaimAndToken(t *testing.T) {
	s := New(cmdrunnertest.NewFake(), fakeDisks{list: []InstallDisk{assess(windows(512 * mib))}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	tok, err := s.Claim()
	if err != nil || tok == "" {
		t.Fatal(tok, err)
	}
	if _, err := s.Claim(); !errors.Is(err, ErrClaimed) {
		t.Errorf("a second claim: %v", err)
	}
	if err := s.Install(ctx, "wrong", "nvme0n1", false, Setup{Hostname: "box"}); !errors.Is(err, ErrClaimed) {
		t.Errorf("install without the token: %v", err)
	}
	if err := s.Install(ctx, tok, "nvme0n1", false, Setup{Hostname: "Not A Name!"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("install with a bad host name: %v", err)
	}
	if err := s.Install(ctx, tok, "nvme0n1", false, Setup{Hostname: "box", Timezone: "Mars/Olympus"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("install with an unknown time zone: %v", err)
	}
	if err := s.DeletePartition(ctx, tok, "nvme0n1", "nvme0n1p3", "nvme0n1p2"); !errors.Is(err, ErrInvalid) {
		t.Errorf("delete with the wrong name typed: %v", err)
	}
	if err := s.DeletePartition(ctx, tok, "nvme0n1", "nvme0n1p3", "nvme0n1p3"); err != nil {
		t.Errorf("delete: %v", err)
	}
	if err := s.PowerOff(ctx, tok); !errors.Is(err, ErrInvalid) {
		t.Errorf("power off before an install: %v", err)
	}
	var none *Service
	if st, err := none.State(ctx); err != nil || st.Active {
		t.Errorf("a host that is not the stick: %+v %v", st, err)
	}
}

func TestListsAreNeverNull(t *testing.T) {
	d := assess(InstallDisk{Name: "sda", Size: 100 * gib, Table: "dos"})
	if d.Partitions == nil || d.Free == nil {
		t.Fatalf("partitions %v free %v: null in JSON", d.Partitions, d.Free)
	}
}

func TestLease(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("ip", "-4", "-j", "route", "show", "default").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		return cmdrunner.Result{Stdout: `[{"dst":"default","gateway":"192.168.1.1","dev":"eth0"}]`}, nil
	})
	f.On("ip", "-4", "-j", "addr", "show", "dev", "eth0").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		return cmdrunner.Result{Stdout: `[{"ifname":"eth0","addr_info":[{"local":"192.168.1.57","prefixlen":24}]}]`}, nil
	})
	resolvConf = filepath.Join(t.TempDir(), "resolv.conf")
	_ = os.WriteFile(resolvConf, []byte("# resolved\nnameserver 192.168.1.1\nnameserver fe80::1\nsearch lan\n"), 0o644)
	s := New(f, fakeDisks{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := s.lease(context.Background())
	if got.Address != "192.168.1.57/24" || got.Gateway != "192.168.1.1" || len(got.DNS) != 1 || got.DNS[0] != "192.168.1.1" {
		t.Errorf("lease = %+v", got)
	}
}

// efibootmgr on a test machine after the first install: NixOS's systemd-boot
// on the Samsung's ESP stayed in front of the new one on the Kingston.
const listing = `BootCurrent: 0003
Timeout: 1 seconds
BootOrder: 0001,0000,0003,0004
Boot0000* Windows Boot Manager	HD(3,GPT,7b8341ba-dfeb-4cb2-a549-37d59fcafc57,0x73ed8800,0x82e000)/\EFI\Microsoft\Boot\bootmgfw.efi
Boot0001* Linux Boot Manager	HD(3,GPT,7b8341ba-dfeb-4cb2-a549-37d59fcafc57,0x73ed8800,0x82e000)/\EFI\systemd\systemd-bootx64.efi
Boot0003* Linux Boot Manager	HD(1,GPT,bca2e54c-c0ef-4bf3-8576-fa62deb9de4b,0x800,0x200000)/\EFI\systemd\systemd-bootx64.efi
Boot0004* UEFI OS	HD(1,GPT,bca2e54c-c0ef-4bf3-8576-fa62deb9de4b,0x800,0x200000)/\EFI\BOOT\BOOTX64.EFI
`

func TestFirstInOrder(t *testing.T) {
	if got, ok := firstInOrder(listing, "BCA2E54C-C0EF-4BF3-8576-FA62DEB9DE4B"); !ok || got != "0003,0001,0000,0004" {
		t.Errorf("the Kingston's loader first: %q %v", got, ok)
	}
	if got, ok := firstInOrder(listing, "7b8341ba-dfeb-4cb2-a549-37d59fcafc57"); !ok || got != "0001,0000,0003,0004" {
		t.Errorf("the Samsung's loader first: %q %v", got, ok)
	}
	if _, ok := firstInOrder(listing, "00000000-0000-0000-0000-000000000000"); ok {
		t.Error("an ESP without an entry")
	}
}

func TestDefinitionsWithoutTheESP(t *testing.T) {
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"00-esp.conf": "[Partition]\nType=esp\n", "10-root.conf": "[Partition]\nType=root\n"})
	old, oldMount := definitions, mountDir
	definitions, mountDir = src, filepath.Join(t.TempDir(), "esp")
	defer func() { definitions, mountDir = old, oldMount }()
	if d, _ := definitionsFor(false); d != src {
		t.Errorf("own ESP: %s", d)
	}
	d, err := definitionsFor(true)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(d)
	if len(entries) != 1 || entries[0].Name() != "10-root.conf" {
		t.Errorf("definitions = %v", entries)
	}
}
