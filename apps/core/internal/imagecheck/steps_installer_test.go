//go:build imagecheck

package imagecheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"homelab-ngx.com/core/internal/imagecheck/fixture"
)

// The installer medium: it boots into installer mode, installs into the
// free space beside another system, and boots as a USB stick next to that
// install and erases a disk. It leaves both installs and the firmware's
// variables as the snapshot installed.
func installerSteps(s *S) {
	r := s.r
	v := r.Version
	stick := s.overlay(filepath.Join(r.Out, "homelab_"+v+".raw"), "stick")
	var disk, beside string
	var region uint64

	s.Step(1, "the installer medium boots "+v+" into installer mode", func() {
		// The newest build's initrd has a storage driver of every kind the
		// image names besides mkosi's defaults.
		newest := r.Versions[len(r.Versions)-1]
		for _, m := range []string{"usb/storage/uas", "mmc/core/mmc_block", "mmc/host/sdhci-pci", "mmc/host/sdhci-acpi", "ata/ata_piix",
			"scsi/megaraid/megaraid_sas", "scsi/mpt3sas/mpt3sas", "message/fusion/mptsas", "scsi/hpsa", "scsi/smartpqi/smartpqi",
			"scsi/aacraid/aacraid", "scsi/arcmsr/arcmsr", "scsi/3w-9xxx", "scsi/3w-sas", "scsi/isci/isci", "scsi/mvsas/mvsas",
			"scsi/pm8001/pm80xx", "ufs/host/ufshcd-pci", "scsi/hv_storvsc", "scsi/vmw_pvscsi", "block/xen-blkfront"} {
			in, err := r.inUKI(newest, m)
			s.dieIf(err, "read the UKI of %s", newest)
			if !in {
				s.die("the initrd of %s has no %s", newest, m)
			}
		}
		uki, err := os.Stat(filepath.Join(r.Out, "homelab_"+newest+".efi"))
		s.dieIf(err, "the UKI of %s", newest)
		if uki.Size() > 200_000_000 {
			s.die("the UKI of %s is %d bytes: over 200 MB", newest, uki.Size())
		}
		fi, err := os.Stat(filepath.Join(r.Out, "homelab_"+v+".raw"))
		s.dieIf(err, "the installer medium")
		size := fi.Size()
		// 1.26 GB in November 2026; the limit catches it growing back.
		if size > 1_500_000_000 {
			s.die("the installer medium is %d bytes: it should stay under 1.5 GB", size)
		}
		// The image tells its installer medium from an installed disk by
		// the medium's disk UUID, the same in every build: the constant it
		// carries must be this.
		s.st.Medium = s.diskID(filepath.Join(r.Out, "homelab_"+v+".raw"))
		for _, f := range []string{"mkosi.extra/usr/lib/udev/rules.d/61-homelab-disk.rules", "mkosi.initrd.conf/mkosi.extra/usr/lib/homelab/loader-disk-wait"} {
			b, err := os.ReadFile(filepath.Join(r.Root, "image", "base", f))
			s.dieIf(err, "read image/base/%s", f)
			named := false
			for _, l := range lines(string(b)) {
				if !regexp.MustCompile(`^ *#`).MatchString(l) && strings.Contains(strings.ToLower(l), s.st.Medium) {
					named = true
				}
			}
			if !named {
				s.die("the installer medium's disk UUID is %s, which image/base/%s does not name", s.st.Medium, f)
			}
		}

		size64 := int64(100 << 30)
		if !r.Build {
			size64 = 80 << 30
		}
		disk = s.newDisk("disk", size64)
		beside = s.path("beside.raw")
		s.dieIf(fixture.WinDisk(beside, "2G", "512M"), "make the Windows disk")
		// A disk sold as 64 GB: 64 billion bytes, 59.6 GiB.
		small := s.newDisk("small", 64_000_000_000)
		// A Windows disk as Windows' setup makes it: the ESP is too small to
		// share.
		win100 := s.path("win100.raw")
		s.dieIf(fixture.WinDisk(win100, "", "100M"), "make the Windows disk with a 100 MB ESP")

		s.boot(bootOpts{}, virtio(stick), virtio(disk), virtio(beside), virtio(small), virtio(win100))
		s.booted(v)
		s.nofailed("")
		if n := s.stickName(); n != "vda" {
			s.die("the disk with the medium's UUID is %s, not vda", n)
		}
		s.runsFrom("vda", "")
		st, raw := s.installerState()
		vda, vdb, vdc := diskNamed(st, "vda"), diskNamed(st, "vdb"), diskNamed(st, "vdc")
		if !st.Active || vda == nil || !vda.InUse || vdb == nil || !vdb.Erase.OK || vdc == nil || !vdc.Beside.OK {
			var ds []string
			for _, d := range list(raw, "disks") {
				ds = append(ds, pick(d, "name", "in_use", "erase", "beside"))
			}
			s.die("unexpected disks: [%s]", strings.Join(ds, ","))
		}
		s.overhttp()
		s.autologin()
		// A disk sold as 64 GB takes an install, where the image's
		// partitions fit it (a version before that says it is too small).
		notes := ""
		if s.has("an install onto a 64 GB disk", yes(at(raw, "min_bytes") != nil && nAt(raw, "min_bytes") <= 64e9)) {
			if d := diskNamed(st, "vdd"); d == nil || !d.Erase.OK {
				s.die("a 64 GB disk is refused: %s", compact(at(find(raw, "disks", "name", "vdd"), "erase")))
			}
			p := s.post("installer/plan", `{"disk":"vdd","erase":true}`, "no plan for a 64 GB disk")
			if !bAt(p, "ok") {
				s.die("no plan for a 64 GB disk: %s", sAt(p, "reason"))
			}
			notes += "; a 64 GB disk offered, with a plan that fits"
		}
		// Next to a Windows with the 100 MB ESP its setup makes there is no
		// install, and the page says why; the disk can still be erased. (A
		// version before the reason named Windows refuses it too.)
		tiny := diskNamed(st, "vde")
		if tiny == nil || !tiny.Erase.OK || tiny.Beside.OK {
			s.die("a disk with a 100 MB ESP: %s", pick(find(raw, "disks", "name", "vde"), "erase", "beside"))
		}
		if s.has("the reason a 100 MB ESP is refused", s.storeOnVar()) {
			if !strings.Contains(tiny.Beside.Reason, "Windows makes one of 100 MB") {
				s.die("the refusal does not say why: %s", tiny.Beside.Reason)
			}
			p := s.call("POST", "installer/plan", `{"disk":"vde","erase":false}`)
			pv := parse(p.Body)
			if bAt(pv, "ok") || !strings.Contains(sAt(pv, "reason"), "400 MB") {
				s.die("a plan next to a 100 MB ESP: %s", p.Body)
			}
			notes += "; free space next to a 100 MB ESP refused with the reason"
		}
		// The installer is one browser's, and not for ever: a second claim
		// is refused, the code on the console takes it over, and the token
		// from before holds nothing after that. A failed boot check never
		// restarts the stick, which has no tries to count. (A version before
		// the take-over has none of this.)
		if s.has("the installer's take-over", s.probe("test -e /run/homelab-installer/takeover-code")) {
			code := s.out("cat /run/homelab-installer/takeover-code")
			if !regexp.MustCompile(`^[0-9]{6}$`).MatchString(code) {
				s.die("the take-over code is not six digits: %s", code)
			}
			first := sAt(s.post("installer/claim", nil, "the first claim was refused"), "token")
			if c := s.call("POST", "installer/claim", nil).Problem(); c != "installer_claimed" {
				s.die("a second claim was not refused")
			}
			if c := s.call("POST", "installer/takeover", `{"code":"x"}`).Problem(); c != "invalid_install" {
				s.die("a wrong take-over code was not refused")
			}
			second := sAt(s.post("installer/takeover", map[string]string{"code": code}, "the console's code did not take the installer over"), "token")
			if !bAt(parse(s.call("GET", "installer", nil, "X-Installer-Token", second).Body), "mine") {
				s.die("the installer is not the taker's")
			}
			if bAt(parse(s.call("GET", "installer", nil, "X-Installer-Token", first).Body), "mine") {
				s.die("the token from before the take-over still holds the installer")
			}
			greeting := s.out("bash -lic true 2>/dev/null")
			if !strings.Contains(greeting, "take-over code: "+code) {
				s.die("the console's greeting does not show the take-over code")
			}
			if !strings.Contains(greeting, "█") {
				s.die("the console's greeting draws no QR code of the address")
			}
			if b := s.out("/usr/lib/systemd/systemd-bless-boot status"); b != "clean" {
				s.die("the stick's boot is counted: %s", b)
			}
			if s.out("systemctl show homelab-boot-check.service -p FailureAction --value") != "none" {
				s.die("a failed boot check would restart the stick")
			}
			if !(st.Machine.Memory > 0 && !st.Machine.SecureBoot && st.Machine.BootEntries) {
				s.die("the machine's facts: %s", compact(at(raw, "machine")))
			}
			notes += "; a second claim refused, taken over with the console's code"
		}
		// The large free region after the NTFS partition, not the 2 GB gap
		// before it.
		var best uint64
		for _, f := range vdc.Free {
			if f.Size > best {
				best, region = f.Size, f.Start
			}
		}
		s.st.Region = region
		s.ok("%d MB, disk UUID %s; verity root, boot-complete, no failed units; the medium in use, a blank disk and free space beside Windows offered; root logged in on the screen and the serial port%s",
			size/1_000_000, s.st.Medium, notes)
	})

	s.Step(2, "install into the free space next to the other system", func() {
		s.install(installArgs{disk: "vdc", hostname: "beside",
			ipv4:   `{"mode":"static","address":"10.0.2.15/24","gateway":"10.0.2.2","dns":["10.0.2.3"]}`,
			region: region})
		s.drop(s.path("small.qcow2"), s.path("win100.raw"))
		t, err := fixture.ReadGPT(beside)
		s.dieIf(err, "read beside's partitions")
		ntfs := t.ByType(fixture.TypeNTFS)
		if len(ntfs) != 1 {
			s.die("the other system's partition is gone: %s", t)
		}
		ingap := 0
		for _, p := range t.Partitions {
			if p.Start > 2048 && p.Start < ntfs[0].Start {
				ingap++
			}
		}
		if ingap != 0 {
			s.die("%d new partitions went into the gap before the other system's: %s", ingap, t)
		}
		if len(t.ByName("homelab-reserved")) > 0 {
			s.die("a temporary partition was left on the disk")
		}
		s.ok("installed into vdc's selected free region (not the gap before the other system) and turned off")
	})

	s.Step(3, "the USB stick boots as the installer next to that install; erase a disk and install", func() {
		// The first install stays attached: its root and verity partitions
		// have the stick's UUIDs, its /var the stick's label, and as virtio
		// disks the kernel finds them before the stick. The firmware would
		// start that install (its boot entry is first), so it is told to
		// start the stick.
		s.boot(bootOpts{order: true}, usb(stick), virtio(disk), virtio(beside))
		s.booted(v)
		name := s.stickName()
		if name == "" {
			s.die("the stick is not among the VM's disks")
		}
		s.runsFrom(name, "")
		st, raw := s.installerState()
		me, vda, vdb := diskNamed(st, name), diskNamed(st, "vda"), diskNamed(st, "vdb")
		if !st.Active || me == nil || !me.InUse || vda == nil || !vda.Erase.OK || vdb == nil || vdb.InUse || !vdb.Erase.OK {
			var ds []string
			for _, d := range list(raw, "disks") {
				ds = append(ds, pick(d, "name", "in_use", "erase"))
			}
			s.die("not the installer, or unexpected disks: active %v [%s]", st.Active, strings.Join(ds, ","))
		}
		s.autologin()
		// Step 13 starts the first install through its boot entry: the
		// firmware must have kept it when it was told an order (see boot).
		t, err := fixture.ReadGPT(beside)
		s.dieIf(err, "read beside's partitions")
		esp := t.Partitions[0].UUID
		if boot := s.out("efibootmgr"); !strings.Contains(strings.ToLower(boot), "gpt,"+esp+",") {
			s.die("the firmware dropped the first install's boot entry: %s", oneLine(boot))
		}
		s.install(installArgs{disk: "vda", erase: true, hostname: "installed"})
		// Every install has partition UUIDs of its own, and none the
		// medium's disk UUID.
		if s.diskID(disk) == s.st.Medium {
			s.die("the installed disk has the medium's disk UUID")
		}
		if u := s.partUUIDs(disk); u == "" || u == s.partUUIDs(beside) {
			s.die("two installs share their /var partition UUID: %s", u)
		}
		s.drop(stick)
		s.ok("the stick (%s) ran from itself next to an install of %s and offered that disk; installed onto vda and turned off", name, v)
	})
}
