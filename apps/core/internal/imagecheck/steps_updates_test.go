//go:build imagecheck

package imagecheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/imagecheck/fixture"
)

// From the configured host: the next version downloads from a release
// server and updates the core, then the image; the third installs broken
// and falls back; with BUILD=1 the host builds an update itself.
func updateSteps(s *S) {
	r := s.r
	v, next, broken := r.Version, r.Next, r.Broken
	disk := s.fromSnap("disk.qcow2", "disk")
	var ds []string
	ds = append(ds, disk)
	for _, n := range []string{"pool1", "pool2", "part", "pool3", "pool4", "pool5"} {
		ds = append(ds, s.fromSnap(n+".qcow2", n))
	}
	// configured was saved powered off: the host starts again where step 8
	// left it.
	up := func() {
		if s.m != nil {
			return
		}
		s.boot(bootOpts{}, virtios(ds)...)
		if !s.daemon(240 * time.Second) {
			s.die("the daemon does not answer")
		}
		s.booted(v)
		s.hostCheck()
	}

	if next != "" {
		s.Step(9, next+" downloads from a release server, then a core update to it through the API", func() {
			up()
			rel, err := fixture.NewRelease(s.dir)
			s.dieIf(err, "start the release server")
			s.rel = rel
			s.putData("/var/lib/homelab/release.asc", string(rel.Public))
			s.must(fmt.Sprintf("printf 'HOMELAB_RELEASE_URL=http://10.0.2.2:%d\\nHOMELAB_RELEASE_KEYRING=/var/lib/homelab/release.asc\\n' > /var/lib/homelab/daemon.env && systemctl restart homelab-daemon", rel.Port))
			if !s.daemon(120 * time.Second) {
				s.die("the daemon does not answer with the release server set")
			}
			// Sums changed after they were signed: refused, nothing staged.
			s.dieIf(rel.Publish(r.Out, next, strings.Repeat("0", 64)+"  homelab_"+next+".efi"), "publish %s", next)
			d := s.op("system-updates/image/download", nil, 15*time.Minute)
			if d.Result != "failed" || !strings.Contains(d.Reason, "not signed") {
				s.die("a tampered release was not refused: %s", deployString(d))
			}
			img := s.getJ("system-updates/image")
			if len(list(img, "available"))+len(list(img, "core.available")) != 0 {
				s.die("a tampered release was staged")
			}
			s.dieIf(rel.Publish(r.Out, next, ""), "publish %s", next)
			d = s.op("system-updates/image/download", nil, 15*time.Minute)
			if d.Result != "healthy" || d.Version != next {
				s.die("download: %s: %s", d.Result, d.Reason)
			}
			img = s.getJ("system-updates/image")
			if !contains(list(img, "available"), next) || !contains(list(img, "core.available"), next) || sAt(img, "release.latest") != next {
				s.die("%s is not staged after its download", next)
			}
			s.ok("a tampered release refused; %s downloaded, verified and staged", next)
			since := s.out("machinectl show site -p Timestamp --value")
			d = s.op("system-updates/image/core", nil, 15*time.Minute)
			if d.Result != "healthy" {
				s.die("core update: %s: %s", d.Result, d.Reason)
			}
			if a := sAt(s.getJ("system-updates/image"), "core.active"); a != next {
				s.die("core %s is not active", next)
			}
			if s.out("machinectl show site -p Timestamp --value") != since {
				s.die("the stack's container was restarted")
			}
			s.hostCheck()
			// A core update restarts Caddy too: an older version's open API
			// is gated now. The core is next's from here on, and has what
			// next has.
			s.running = next
			signed := ""
			if s.has("the dashboard's sign-in", s.hasSignin) {
				s.signin()
				s.stackRefused("site")
				signed = "; the dashboard behind its sign-in, refused to the stack"
			}
			s.running = v
			s.ok("core %s active, no reboot, the stack's container untouched%s", next, signed)
		})

		s.Step(10, "an image update to "+next+" through the API", func() {
			s.mark()
			_, _ = s.tryOp("system-updates/image/install", `{"reboot":"now"}`, 15*time.Minute)
			// The install reboots the host: wait for the next boot.
			s.newboot(20 * time.Minute)
			if !s.daemon(240 * time.Second) {
				s.die("the daemon does not answer after the update to %s", next)
			}
			s.booted(next)
			if !strings.Contains(s.out("systemd-sysext status --no-legend"), "homelab-core") {
				s.die("the core extension is not merged on %s", next)
			}
			s.fixedIDs()
			// next has no /pool: a pool the first version mounted there is
			// mounted again by hand, under its name, and the stack is given
			// its new path.
			if s.st.Pool == "/pool" && s.has("pools mounted under /var/mnt", s.probe("test -L /mnt")) {
				s.st.Pool = "/var/mnt/pool"
				s.ask("POST", "storage/mount", `{"devices":["/dev/vdb","/dev/vdc"]}`, "mount refused on "+next)
				s.put("stacks/site/variables/SITE_DIR", map[string]string{"value": s.st.Pool + "/site"}, "variable refused on "+next)
				_, _ = s.try("POST", "stacks/site/stop", nil)
				if !s.until(90*time.Second, s.stopped) {
					s.die("the stack's stop did not end on %s", next)
				}
				s.ask("POST", "stacks/site/start", nil, "start refused on "+next)
			}
			s.hostCheck()
			// A release before the VPN device's keys left that part of step
			// 7 to next.
			later := ""
			if s.st.VPNDevices == 1 {
				yes, err := s.hasDeviceKeys()()
				if err != nil || !yes {
					s.die("%s does not make a VPN device's keys", next)
				}
				s.deviceKeys()
				s.hostCheck()
				later = ", and a VPN device with keys it made"
			}
			s.stackRace()
			s.ok("booted %s with everything in place%s; the stack's link kept its address through network reloads", next, later)
			// The way back by hand: the version before is still in its slot.
			st := s.post("system-updates/image/boot", map[string]string{"version": v, "mode": "default"}, "making "+v+" the default was refused")
			if sAt(st, "default") != v {
				s.die("%s is not the default: %s", v, pick(st, "default", "oneshot"))
			}
			st = s.post("system-updates/image/boot", `{"version":"","mode":"default"}`, "clearing the default was refused")
			if sAt(st, "default") != "" {
				s.die("the default is still set: %s", pick(st, "default", "oneshot"))
			}
			if c := s.call("POST", "system-updates/image/boot", `{"version":"0.0.0.1","mode":"once"}`).Problem(); c != "no_such_version" {
				s.die("a version that is not installed was accepted")
			}
			s.mark()
			st = s.post("system-updates/image/boot", map[string]any{"version": v, "mode": "once", "reboot": true}, "booting "+v+" once was refused")
			if sAt(st, "oneshot") != v {
				s.die("%s is not set to boot once: %s", v, pick(st, "default", "oneshot"))
			}
			s.newboot(20 * time.Minute)
			if !s.daemon(120 * time.Second) {
				s.die("the daemon does not answer on %s", v)
			}
			s.booted(v)
			s.rebootVM()
			s.booted(next)
			s.hostCheck()
			s.ok("%s booted once more from the API, then %s again", v, next)
			s.poolLifecycle()
			s.backupLifecycle()
			if !s.st.SMTPRelay {
				s.testSMTPRelay()
				if s.st.SMTPRelay {
					s.hostCheck()
				}
			}
		})
	} else {
		s.Step(9, "core and image updates skipped: image/out has one version", func() {})
	}

	if broken != "" {
		s.Step(11, broken+" installs in one operation; its broken image falls back to "+next, func() {
			// A downloaded core is in use after a restart, unless it names a
			// newer image than the host runs. (An image from before that
			// leaves it staged.)
			atboot := false
			if s.has("a downloaded core in use after a restart", s.probe("test -x /usr/lib/homelab/core-at-boot")) {
				atboot = true
				need := next + ".1"
				newer := filepath.Join(s.dir, "homelab-core_"+need+".raw")
				if err := fixture.CoreNeeding(filepath.Join(r.Out, "core", "homelab-core_"+next+".raw"), "9999", newer, s.dir); err != nil {
					if strings.Contains(err.Error(), "names no minimum image") {
						s.die("core %s names no minimum image", next)
					}
					s.die("make a core for a newer image: %v", err)
				}
				s.putFile(newer, "/var/lib/homelab/updates/core/homelab-core_"+need+".raw")
				_ = os.Remove(newer)
				d := s.op("system-updates/image/core", nil, 15*time.Minute)
				if d.Result != "failed" || !strings.Contains(d.Reason, "needs image 9999") {
					s.die("a core for a newer image was not refused: %s", deployString(d))
				}
				s.rebootVM()
				s.booted(next)
				if !s.daemon(120 * time.Second) {
					s.die("the daemon does not answer")
				}
				if a := sAt(s.getJ("system-updates/image"), "core.active"); a != next {
					s.die("a core for a newer image was applied at boot")
				}
				s.must("rm /var/lib/homelab/updates/core/homelab-core_" + need + ".raw")
				s.putFile(filepath.Join(r.Out, "core", "homelab-core_"+broken+".raw"), "/var/lib/homelab/updates/core/homelab-core_"+broken+".raw")
				s.rebootVM()
				s.booted(next)
				if !s.daemon(120 * time.Second) {
					s.die("the daemon does not answer on the core applied at boot")
				}
				if a := sAt(s.getJ("system-updates/image"), "core.active"); a != broken {
					s.die("the downloaded core %s is not in use after a restart: %s", broken, oneLine(s.out("journalctl -b -u homelab-core-update --no-pager | tail -5")))
				}
				s.nofailed("")
				s.ok("a core for a newer image refused and left staged; the downloaded core %s in use after a restart", broken)
			}
			s.stageImage(broken)
			s.putFile(filepath.Join(r.Out, "core", "homelab-core_"+broken+".raw"), "/var/lib/homelab/updates/core/homelab-core_"+broken+".raw")
			d := s.op("system-updates/image/release", nil, 15*time.Minute)
			if d.Result != "healthy" || d.Version != broken {
				s.die("%s was not installed: %s", broken, deployString(d))
			}
			img := s.getJ("system-updates/image")
			if sAt(img, "core.active") != broken || !bAt(img, "pending_reboot") {
				s.die("after the install: %s", pick(img, "core.active", "pending_reboot", "installed"))
			}
			s.ok("image %s in the free slot and core %s running, from one call", broken, broken)
			// Corrupt the new slot's root: its verity check fails, so does
			// the boot.
			rr, err := s.g.Run(bg(), "bash -s "+broken, strings.NewReader(corruptScript))
			if err != nil || rr.Code != 0 {
				s.die("could not corrupt the new slot: %v %s", err, strings.TrimSpace(rr.Stderr))
			}
			// Three failed tries of the broken version, then the one before.
			s.restart(30 * time.Minute)
			if !s.daemon(240 * time.Second) {
				s.die("the daemon does not answer after the fall back")
			}
			s.booted(next)
			if f := sAt(s.getJ("system-updates/image"), "fell_back_from"); f != broken {
				s.die("the daemon does not say it fell back from %s", broken)
			}
			s.hostCheck()
			s.ok("back on %s by itself; the daemon reports the fall back from %s", next, broken)

			// A daemon that cannot start (a file where its state folder
			// was), on a version that was good before: the boot check tries
			// the core before, fails, and leaves the host up.
			s.must("systemctl stop homelab-daemon && mv /var/lib/homelab-daemon /var/lib/homelab-daemon.keep && touch /var/lib/homelab-daemon")
			s.restart(20 * time.Minute)
			bootN := s.m.Current()
			bootID := s.out("cat /proc/sys/kernel/random/boot_id")
			s.until(900*time.Second, func() bool { return s.out("systemctl is-failed homelab-boot-check.service") == "failed" })
			if s.out("systemctl is-failed homelab-boot-check.service") != "failed" {
				s.die("the boot check did not fail with the daemon broken: %s", s.out("systemctl is-active homelab-boot-check.service"))
			}
			// The host must not restart now: QEMU would report the reset.
			ctx, cancel := ctxFor(30 * time.Second)
			err = s.m.WaitReset(ctx, bootN)
			cancel()
			if err == nil {
				s.die("the host restarted after the boot check failed on a good version")
			}
			if s.out("cat /proc/sys/kernel/random/boot_id") != bootID {
				s.die("the host restarted after the boot check failed on a good version")
			}
			if !s.test(`grep -q "daemon does not answer" /run/homelab/boot-check-failed`) {
				s.die("no reason left: %s", s.out("cat /run/homelab/boot-check-failed"))
			}
			if l := s.out("readlink /var/lib/extensions/homelab-core.raw"); l != "../extensions.d/homelab-core_"+broken+".raw" {
				s.die("the core extension was not put back: %s", l)
			}
			s.must("rm /var/lib/homelab-daemon && mv /var/lib/homelab-daemon.keep /var/lib/homelab-daemon && systemctl restart homelab-daemon")
			if !s.daemon(120 * time.Second) {
				s.die("the daemon does not answer after its repair")
			}
			if !strings.Contains(sAt(s.getJ("system-updates/image"), "boot_check"), "daemon does not answer") {
				s.die("the daemon does not report the failed boot check")
			}
			s.ok("a daemon that cannot start: the host stayed up with the boot check failed, and says why once repaired")

			// A core extension that does not start (its file overwritten):
			// the boot check merges the one before.
			s.must("dd if=/dev/urandom of=/var/lib/extensions.d/homelab-core_" + broken + ".raw bs=1M count=4 conv=notrunc,fsync status=none")
			s.restart(20 * time.Minute)
			if !s.daemon(600 * time.Second) {
				s.die("the daemon does not answer on the core before")
			}
			s.booted(next)
			img = s.getJ("system-updates/image")
			if sAt(img, "core.fell_back_from") != broken || sAt(img, "core.active") != next || sAt(img, "boot_check") != "" {
				s.die("after a broken core: %s", pick(img, "core", "boot_check"))
			}
			s.hostCheck()
			again := ""
			if atboot {
				// No boot tries that core again by itself.
				if !s.test("grep -qFx '" + broken + "' /var/lib/homelab/core-failed") {
					s.die("the core that did not start is not written down")
				}
				s.rebootVM()
				s.booted(next)
				if !s.daemon(120 * time.Second) {
					s.die("the daemon does not answer")
				}
				if a := sAt(s.getJ("system-updates/image"), "core.active"); a != next {
					s.die("the core that did not start was used again: %s", oneLine(s.out("journalctl -b -u homelab-core-update --no-pager | tail -5")))
				}
				again = ", and not tried again at the next boot"
			}
			s.ok("a core that does not start: core %s back at boot, and the daemon reports it%s", next, again)
		})
	} else {
		s.Step(11, "fall back skipped: image/out has fewer than three versions", func() {})
	}

	if r.Build {
		s.Step(12, "the host builds an update from main", func() {
			up()
			// With the tags: build.sh versions the build above the newest
			// release (<tag>.<commit time>). Without them it is 0.0.<commit
			// time>, older than the installed version, and staging prunes
			// it. The versions in image/out must be older than HEAD's commit
			// time for the build to be newer.
			bundle := filepath.Join(s.dir, "src.bundle")
			if out, err := exec.Command("git", "-C", r.Root, "bundle", "create", bundle, "HEAD", "--tags").CombinedOutput(); err != nil {
				s.die("git bundle: %v: %s", err, out)
			}
			s.putFile(bundle, "/var/lib/homelab/src.bundle")
			_ = os.Remove(bundle)
			s.must("echo HOMELAB_CORE_SOURCE=git+file:///var/lib/homelab/src.bundle > /var/lib/homelab/daemon.env && systemctl restart homelab-daemon")
			if !s.daemon(120 * time.Second) {
				s.die("the daemon does not answer with its source set")
			}
			// The memory the whole host uses while it builds: what a host
			// that builds its own updates needs.
			s.putData("/var/tmp/memwatch.sh", "while :; do free -m | awk '/^Mem/ {print $3}' >> /var/tmp/mem.log; sleep 2; done\n")
			s.must("systemd-run -q --unit=memwatch sh /var/tmp/memwatch.sh")
			// Twice: the second build reuses mkosi's packages and OS tree.
			// Neither copies mkosi's outputs across mounts.
			var times []string
			built := ""
			for n := 1; n <= 2; n++ {
				since := s.must("date +%s")
				start := time.Now()
				d := s.op("system-updates/image/build", nil, 60*time.Minute)
				if d.Result != "healthy" {
					s.die("build %d: %s: %s", n, d.Result, d.Reason)
				}
				built = d.Version
				img := s.getJ("system-updates/image")
				if !contains(list(img, "core.available"), built) && !contains(list(img, "available"), built) {
					s.die("%s is not staged", built)
				}
				// systemd logs the unit's memory peak when it stops.
				s.sh("for _ in $(seq 1 30); do systemctl is-active -q homelab-core-deploy || break; sleep 1; done")
				journal := s.out("journalctl -q -o cat -u homelab-core-deploy --since=@" + since)
				// Saving a new incremental cache copies once (/out to
				// /cache); the outputs are renamed into /out.
				for _, l := range lines(journal) {
					if strings.Contains(l, "falling back to copying") && strings.Contains(l, " to /out/") {
						s.die("build %d copied mkosi's outputs: %s", n, l)
					}
				}
				downloads := strings.Count(journal, "Retrieving packages")
				if n == 2 && downloads > 0 {
					s.die("the second build downloaded packages again")
				}
				peak := "peak unknown"
				if m := regexp.MustCompile(`[0-9.]*[KMG] memory peak`).FindAllString(journal, -1); len(m) > 0 {
					peak = m[len(m)-1]
				}
				times = append(times, fmt.Sprintf("build %d: %d s, %d package downloads, unit %s", n, int(time.Since(start).Seconds()), downloads, peak))
			}
			s.sh("systemctl stop memwatch")
			s.ok("built and staged %s twice (%s); memory in use peaked at %s of %d MiB", built, strings.Join(times, "; "),
				s.out("sort -n /var/tmp/mem.log | tail -1"), r.MemMiB)
		})
	}
}

const corruptScript = `set -e
# sysupdate has just labelled the slot: let udev catch up.
for _ in $(seq 1 15); do
  udevadm settle
  root=$(lsblk -lnpo PATH,PARTLABEL,PARTTYPE | awk -v l="homelab_$1" '$2 == l && $3 == "4f68bce3-e8cd-4db1-96e7-fbcaf984b709" {print $1}')
  [ -n "$root" ] && break
  sleep 2
done
test -n "$root"
dd if=/dev/urandom of="$root" bs=1M count=8 conv=notrunc,fsync status=none
`

func contains(l []any, s string) bool {
	for _, x := range l {
		if str(x) == s {
			return true
		}
	}
	return false
}
