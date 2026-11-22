//go:build imagecheck

package imagecheck

import (
	"path/filepath"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/imagecheck/fixture"
	"homelab-ngx.com/core/internal/imagecheck/vm"
)

type vmDisk = vm.Disk

// The free-space install of step 2 boots alone, beside the other system.
func besideSteps(s *S) {
	v := s.r.Version
	beside := s.fromSnap("beside.raw", "beside")
	s.Step(13, "the free-space install boots alone, beside the other system", func() {
		s.boot(bootOpts{}, virtio(beside))
		s.booted(v)
		s.runsFrom("vda", "")
		s.ownID()
		if h := s.out("cat /proc/sys/kernel/hostname"); h != "beside" {
			s.die("the host name is %s", h)
		}
		if f := s.espFiles("/dev/vda1"); f != "WINDOWS-BOOTMGR WINDOWS-FALLBACK " {
			s.die("the other system's loaders changed: %s", f)
		}
		if !strings.Contains(s.out("lsblk -no PARTTYPENAME /dev/vda"), "Microsoft basic data") {
			s.die("the other system's partition is gone")
		}
		if !strings.Contains(s.out("bootctl list"), "id: auto-windows") {
			s.die("the boot menu has no Windows entry")
		}
		if !s.test(`grep -q "^Address=10.0.2.15/24" /etc/systemd/network/10-homelab-wired.network`) {
			s.die("the installer's static address is not set")
		}
		if !grep(`inet 10\.0\.2\.15/24 .* global`, s.out("ip -4 -o addr show")) {
			s.die("the static address is not on the interface")
		}
		if !s.isDefault() {
			s.die("the system is not the boot menu's default")
		}
		s.ok("boots alone with its static address, as the menu's default next to Windows Boot Manager; its loaders and partition untouched")
	})
}

// isDefault: the boot menu's default is the system's entry.
func (s *S) isDefault() bool {
	ls := lines(s.out("bootctl list"))
	for i, l := range ls {
		if strings.Contains(l, "id: homelab_") && i > 0 && strings.Contains(ls[i-1], "(default)") {
			return true
		}
	}
	return false
}

// The version under test from its own medium, on a second machine (fresh
// firmware): a disk whose boot loader goes on another disk's ESP, the stick
// left in, an install over it, its machine ID, other controllers, and a
// nix partition.
func underSteps(s *S) {
	r := s.r
	u := r.Under
	stick := s.overlay(filepath.Join(r.Out, "homelab_"+u+".raw"), "stick")
	s.st.Medium = s.diskID(filepath.Join(r.Out, "homelab_"+u+".raw"))
	sys := s.newDisk("sys", 80<<30)
	win2 := s.path("win2.raw")
	var crossdisk, id string
	var drivers bool

	s.Step(14, u+": a blank disk whose boot loader goes on another disk's ESP", func() {
		s.dieIf(fixture.WinDisk(win2, "", "512M"), "make the Windows disk")
		s.boot(bootOpts{}, virtio(stick), virtio(sys), virtio(win2))
		s.booted(u)
		s.install(installArgs{disk: "vdb", erase: true, hostname: "crossdisk", esp: "vdc1"})
		// The stick stays in, now as a USB stick. The firmware's boot order
		// has the install first, as the installer left it.
		s.boot(bootOpts{}, virtio(sys), virtio(win2), usb(stick))
		s.booted(u)
		s.runsFrom("vda", "vdb")
		s.fixedIDs()
		s.ownID()
		crossdisk = s.id
		// Where this version keeps the store, for step 19.
		s.st.OnVar = s.has("the Nix store on /var", s.storeOnVar())
		if h := s.out("cat /proc/sys/kernel/hostname"); h != "crossdisk" {
			s.die("the host name is %s", h)
		}
		if b := s.out("ls /boot/EFI/Linux/ >/dev/null; findmnt -no SOURCE /boot"); !grep(`(?m)^/dev/vdb1$`, b) {
			s.die("/boot is not the other disk's ESP: %s", s.out("findmnt -no SOURCE /boot"))
		}
		if !grep(`(?m)^`+strings.ReplaceAll("homelab_"+u+".efi", ".", `\.`)+`$`, s.out("ls /boot/EFI/Linux/")) {
			s.die("the kernel is not on the other disk's ESP")
		}
		parts, err := s.g.Out(bg(), "lsblk -no PARTTYPENAME /dev/vda")
		if err != nil {
			s.die("could not list the system disk's partitions")
		}
		if strings.Contains(parts, "EFI System") {
			s.die("the system disk got an ESP of its own")
		}
		if !strings.Contains(s.out("bootctl list"), "id: auto-windows") {
			s.die("the boot menu has no Windows entry")
		}
		if !s.isDefault() {
			s.die("the system is not the boot menu's default")
		}
		if f := s.espFiles("/dev/vdb1"); f != "WINDOWS-BOOTMGR WINDOWS-FALLBACK " {
			s.die("the other system's loaders changed: %s", f)
		}
		s.ok("boots from the other disk's ESP, mounted at /boot; Windows in its menu, its loaders untouched")
	})

	s.Step(15, "that boot had the stick left in", func() {
		if s.stickName() == "" {
			s.die("the stick is not among the VM's disks")
		}
		if !s.daemon(120 * time.Second) {
			s.die("the daemon does not answer with the stick in")
		}
		if bAt(s.getJ("installer"), "active") {
			s.die("the installed host runs the installer with the stick in")
		}
		if h := s.out("cat /proc/sys/kernel/hostname"); h != "crossdisk" {
			s.die("the host name is %s", h)
		}
		if s.out("cat /etc/machine-id") != crossdisk {
			s.die("the machine ID changed with the stick in")
		}
		if b := s.out("ls /boot/EFI/Linux/ >/dev/null; findmnt -no SOURCE /boot"); !grep(`(?m)^/dev/vdb1$`, b) {
			s.die("/boot is not the other disk's ESP: %s", s.out("findmnt -no SOURCE /boot"))
		}
		s.nologin(" with the stick in")
		s.nofailed("")
		s.ok("runs from its own disk and the other disk's ESP, the stick (%s) unused; nobody logged in on a console", s.stickName())
	})

	s.Step(16, "the stick boots next to that install, installs over it and restarts into the new system", func() {
		// A machine set to start from USB first, and without a firmware
		// UUID. The other disk and its ESP are away: the new install brings
		// its own.
		s.stop()
		s.drop(win2)
		s.boot(bootOpts{order: true, noUUID: true}, usb(stick), virtio(sys))
		s.booted(u)
		name := s.stickName()
		if name == "" {
			s.die("the stick is not among the VM's disks")
		}
		s.runsFrom(name, "")
		st, raw := s.installerState()
		me, vda := diskNamed(st, name), diskNamed(st, "vda")
		if !st.Active || me == nil || !me.InUse || vda == nil || vda.InUse || !vda.Erase.OK {
			var ds []string
			for _, d := range list(raw, "disks") {
				ds = append(ds, pick(d, "name", "in_use", "erase"))
			}
			s.die("not the installer, or unexpected disks: active %v [%s]", st.Active, strings.Join(ds, ","))
		}
		s.autologin()
		s.installTo(installArgs{disk: "vda", erase: true, hostname: "again"})
		if !bAt(s.getJ("installer"), "can_restart") {
			s.die("the installer cannot restart into the new system: %s", oneLine(s.out("efibootmgr")))
		}
		s.mark()
		_, _ = s.try("POST", "installer/restart", nil, "X-Installer-Token", s.token)
		// The same machine comes back by itself, the stick still first in
		// the order the firmware was told.
		ctx, cancel := ctxFor(480 * time.Second)
		err := s.m.WaitReset(ctx, s.marked)
		cancel()
		if err != nil {
			if !s.m.Alive() {
				s.die("the VM turned off instead of restarting")
			}
			s.die("the machine did not restart (serial log %s)", s.serial)
		}
		s.newboot(480 * time.Second)
		s.booted(u)
		if !s.daemon(120 * time.Second) {
			s.die("the daemon does not answer after the restart")
		}
		if bAt(s.getJ("installer"), "active") {
			s.die("the restart started the installer again, not the new system")
		}
		if s.stickName() == "" {
			s.die("the stick is not among the VM's disks")
		}
		s.runsFrom("vda", "")
		if h := s.out("cat /proc/sys/kernel/hostname"); h != "again" {
			s.die("the host name is %s", h)
		}
		s.nologin(" with the stick in")
		s.nofailed("")
		s.ok("the stick (%s) ran from itself and offered the installed disk; after the install the page's restart brought up the new system from vda, the stick still in", name)
	})

	esp := s.path("esp.raw")
	// ctl: the system with its disk behind another controller. The
	// machine's firmware reads none of them, so the boot loader and kernel
	// start from a copy of the system's ESP on a virtio disk; the initrd
	// then has to find the root and /var behind the controller.
	ctl := func(kind vm.Kind) {
		s.boot(bootOpts{order: true, noUUID: true}, virtio(esp), vm.Disk{Kind: kind, Path: sys})
		s.booted(u)
		s.runsFrom("sda", "vda")
	}

	s.Step(17, "the machine ID without a firmware UUID, and one for every install", func() {
		s.ownID()
		id = s.id
		s.stop()
		s.drop(stick)
		// With the storage drivers in its initrd (a version before the
		// image named them has not), the system also has to start with its
		// disk behind other controllers. This boot is the first of them.
		storageDrivers := func() (bool, error) {
			for _, m := range []string{"scsi/vmw_pvscsi", "scsi/megaraid/megaraid_sas", "usb/storage/uas"} {
				in, err := r.inUKI(u, m)
				if err != nil || !in {
					return false, err
				}
			}
			return true, nil
		}
		note := ""
		if s.has("the storage drivers the image names, in the initrd", storageDrivers) {
			drivers = true
			note = ", its disk behind VMware's paravirtual SCSI"
			s.dieIf(fixture.ESPDisk(sys, esp), "copy the system's ESP")
			ctl(vm.PVSCSI)
		} else {
			s.boot(bootOpts{noUUID: true}, virtio(sys))
			s.booted(u)
		}
		if got := s.out("cat /etc/machine-id"); got != id {
			s.die("the machine ID changed over a boot: %s, was %s", got, id)
		}
		if s.out("cat /var/lib/homelab/machine-id") != id {
			s.die("the machine ID on /var changed over a boot")
		}
		s.nofailed("")
		s.ok("the same on the next boot%s; %d installs, each with its own", note, r.idCount())
	})

	s.Step(18, "the system boots with its disk behind other controllers", func() {
		if !drivers {
			s.ok("skipped: %s is from before the image named its storage drivers", u)
			return
		}
		for _, c := range []vm.Kind{vm.MegaSAS, vm.UAS} {
			s.stop()
			ctl(c)
			if s.out("cat /etc/machine-id") != id {
				s.die("behind %s another system booted", c)
			}
			s.nofailed(" behind " + string(c))
		}
		s.ok("the same system with its disk behind a MegaRAID and USB UAS")
	})

	s.Step(19, "a disk with a nix partition keeps its store there", func() {
		// A host installed before the store moved to /var has a partition
		// named nix and its store on it. The disk gets one at its end,
		// formatted from the system itself (that boot has no /nix, and says
		// so without restarting).
		s.stop()
		if !s.st.OnVar {
			s.ok("skipped: %s has the store on a partition of its own already", u)
			return
		}
		raw := s.path("sys.raw")
		s.dieIf(vm.QemuImg("convert", "-q", "-O", "raw", sys, raw), "make a raw copy of the system disk")
		s.drop(sys, esp)
		s.dieIf(vm.QemuImg("resize", "-q", "-f", "raw", raw, "+1G"), "grow the system disk")
		if _, err := fixture.Run("", "sfdisk", "-q", "--relocate", "gpt-bak-std", raw); err != nil {
			s.die("%v", err)
		}
		if _, err := fixture.Run("type=073D2B22-9F7E-4817-8D45-890277F7DD55, name=nix\n", "sfdisk", "-q", "--append", raw); err != nil {
			s.die("%v", err)
		}
		s.boot(bootOpts{noUUID: true}, virtio(raw))
		nixpart := ""
		for _, l := range lines(s.out("lsblk -lno NAME,PARTLABEL /dev/vda")) {
			if f := strings.Fields(l); len(f) == 2 && f[1] == "nix" {
				nixpart = f[0]
			}
		}
		if nixpart == "" {
			s.die("the nix partition is not on the disk: %s", oneLine(s.out("lsblk -lno NAME,PARTLABEL /dev/vda")))
		}
		if !s.test("mkfs.ext4 -q /dev/" + nixpart) {
			s.die("could not format /dev/%s", nixpart)
		}
		s.stop()
		s.boot(bootOpts{noUUID: true}, virtio(raw))
		s.booted(u)
		if src := s.out("findmnt -no SOURCE -M /nix"); src != "/dev/"+nixpart {
			s.die("/nix is not the nix partition: %s", src)
		}
		if s.out("cat /etc/machine-id") != id {
			s.die("another system booted")
		}
		if !s.daemon(120 * time.Second) {
			s.die("the daemon does not answer")
		}
		if st := s.getJ("stacks-store"); !bAt(st, "own_partition") {
			s.die("the daemon does not see the store's own partition: %s", compact(st))
		}
		s.nofailed("")
		s.ok("/nix is the disk's nix partition (%s)", nixpart)
	})
}
