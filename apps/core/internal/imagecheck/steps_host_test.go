//go:build imagecheck

package imagecheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/imagecheck/fixture"
)

// The installed disk: it boots with its core, a pool, a stack and the host
// settings, all of which hold after a reboot. It leaves the disks, powered
// off, as the snapshot configured.
func hostSteps(s *S) {
	r := s.r
	v := r.Version
	disk := s.fromSnap("disk.qcow2", "disk")
	var pools []string
	for _, d := range []struct {
		name string
		size int64
	}{{"pool1", 8 << 30}, {"pool2", 8 << 30}, {"part", 1 << 30}, {"pool3", 4 << 30}, {"pool4", 4 << 30}, {"pool5", 4 << 30}} {
		pools = append(pools, s.newDisk(d.name, d.size))
	}
	var sitewait time.Duration

	s.Step(4, "the installed disk boots "+v+" with its core, its name, machine ID, time zone, SSH key and a domain", func() {
		ds := []string{disk}
		ds = append(ds, pools...)
		s.boot(bootOpts{}, virtios(ds)...)
		s.booted(v)
		s.runsFrom("vda", "")
		s.ownID()
		s.st.Installed = s.id
		if !s.test(`test "$(findmnt -no SIZE /var | tr -d G | cut -d. -f1)" -gt 30`) {
			s.die("/var does not fill the disk")
		}
		// The Nix store is on /var: no partition of its own takes room from
		// it (a release from before that, installed first in a release's
		// check, has one).
		if s.has("the Nix store on /var", s.storeOnVar()) {
			s.st.OnVar = true
			if src := s.out("findmnt -no SOURCE -M /nix"); !grep(`\[/nix\]$`, src) {
				s.die("/nix is not /var's folder: %s", src)
			}
			t, err := fixture.ReadGPT(disk)
			s.dieIf(err, "read the disk's partitions")
			if len(t.ByName("nix")) > 0 {
				s.die("the install made a nix partition")
			}
		} else if !s.test("mountpoint -q /nix") {
			s.die("/nix is not mounted")
		}
		if !strings.Contains(s.out("systemd-sysext status --no-legend"), "homelab-core") {
			s.die("the core extension is not merged")
		}
		if !s.daemon(120 * time.Second) {
			s.die("the daemon does not answer")
		}
		if bAt(s.getJ("installer"), "active") {
			s.die("the installed host runs the installer")
		}
		s.installedLogin("")
		if h := s.out("cat /proc/sys/kernel/hostname"); h != "installed" {
			s.die("the host name is %s, not the installer's", h)
		}
		if h := sAt(s.getJ("host-settings"), "host.hostname"); h != "installed" {
			s.die("the installer's host name is not in the settings")
		}
		if !strings.Contains(s.out("readlink /etc/localtime"), "Europe/Stockholm") {
			s.die("the installer's time zone is not set")
		}
		// (A release from before the guidance has none.)
		if s.has("the coding agents' guidance", s.probe("test -e /usr/share/homelab/agent/brief.md")) &&
			!s.test(`grep -q "^# This machine runs homelab-ngx" /etc/claude-code/CLAUDE.md && grep -q "^name: homelab" /etc/claude-code/.claude/skills/homelab/SKILL.md /etc/codex/skills/homelab/SKILL.md && grep -q /usr/share/homelab/agent/brief.md /etc/codex/config.toml /etc/opencode/opencode.json && test -s "$(jq -r ".instructions[0]" /etc/opencode/opencode.json)"`) {
			s.die("the coding agents' guidance is not reachable from /etc")
		}
		if err := s.key2(); err != nil {
			s.die("the SSH key given to the installer does not let root in: %v", err)
		}
		s.nofailed("")
		s.overhttp()
		if m := sAt(s.getJ("host-settings"), "managed"); m != "image" {
			s.die("the daemon does not see an image host")
		}
		// Wake-on-LAN: the wired card takes the image's link file under the
		// name it would have had, and the settings show its MAC address. (A
		// version before the link file has neither.)
		wol := ""
		if s.has("Wake-on-LAN for the wired card", s.probe("test -e /usr/lib/systemd/network/80-wired.link")) {
			nic := ""
			for _, n := range lines(s.out("ls /sys/class/net")) {
				if strings.HasPrefix(n, "en") || strings.HasPrefix(n, "eth") {
					nic = n
					break
				}
			}
			link := s.out("udevadm info -q property /sys/class/net/" + nic)
			if !grep(`(?m)^ID_NET_LINK_FILE=/usr/lib/systemd/network/80-wired\.link$`, link) {
				s.die("%s does not take 80-wired.link: %s", nic, grepLines("LINK_FILE", link))
			}
			if !grep(`(?m)^ID_NET_NAME_PATH=`+nic+`$`, link) {
				s.die("the wired card is named %s, not by its path: %s", nic, grepLines("ID_NET_NAME", link))
			}
			mac := s.out("cat /sys/class/net/" + nic + "/address")
			if find(s.getJ("host-settings"), "wired", "mac", mac) == nil {
				s.die("the settings do not show %s's MAC address %s", nic, mac)
			}
			wol = "; " + nic + " takes 80-wired.link (Wake-on-LAN) under its own name, its MAC address in the settings"
		}
		s.put("host-settings/domain", map[string]string{"domain": r.Domain}, "domain refused")
		if !s.until(60*time.Second, s.served) {
			s.die("the dashboard is not served at homelab.%s", r.Domain)
		}
		signed := " (no sign-in on " + v + ")"
		if s.has("the dashboard's sign-in", s.hasSignin) {
			s.signin()
			signed = " behind its sign-in"
		}
		s.ok("daemon %s, dashboard over HTTP and at https://homelab.%s%s%s", sAt(s.getJ("version"), "version"), r.Domain, signed, wol)
		if s.fixedIDs() {
			s.ok("caddy, homelab-daemon and homelab-nixd have their fixed ids")
		}
	})

	s.Step(5, "a bcachefs pool through the daemon", func() {
		s.must("bcachefs format -q --replicas=2 /dev/vdb /dev/vdc >/dev/null")
		// Pools mount under /var/mnt, by name. A release from before that
		// (0.2.5, the first version of a check that updates from it) has
		// /pool in its image and takes a path.
		pool, guarded := "/var/mnt/pool", ""
		if s.has("pools mounted under /var/mnt", s.probe("test -L /mnt")) {
			s.ask("POST", "storage/mount", `{"devices":["/dev/vdb","/dev/vdc"]}`, "mount refused")
			if !strings.Contains(s.out("findmnt -no FSTYPE "+pool), "bcachefs") {
				s.die("%s is not mounted", pool)
			}
			if !s.test("touch /mnt/pool/.check && rm /var/mnt/pool/.check") {
				s.die("/mnt/pool is not the pool")
			}
			if c := s.code("POST", "storage/mount", `{"devices":["/dev/vdb"],"name":"../x"}`); c != 422 && c != 400 {
				s.die("a pool name with a path in it answered %d", c)
			}
			// Unmounted, its directory takes no writes: they would land on
			// /var.
			s.ask("POST", "storage/unmount", map[string]string{"mountpoint": pool}, "unmount refused")
			if !s.test("! touch " + pool + "/x 2>/dev/null") {
				s.die("the unmounted %s can be written to", pool)
			}
			s.ask("POST", "storage/mount", `{"devices":["/dev/vdb","/dev/vdc"]}`, "the second mount was refused")
			if !strings.Contains(s.out("findmnt -no FSTYPE "+pool), "bcachefs") {
				s.die("%s is not mounted again under the name it had", pool)
			}
			guarded = "; unmounted, its directory takes no writes"
		} else {
			pool = "/pool"
			s.ask("POST", "storage/mount", `{"devices":["/dev/vdb","/dev/vdc"],"mountpoint":"/pool"}`, "mount refused")
			if !strings.Contains(s.out("findmnt -no FSTYPE /pool"), "bcachefs") {
				s.die("/pool is not mounted")
			}
		}
		s.st.Pool = pool
		s.ok("pool mounted at %s (%s)%s", pool, s.out("bcachefs version"), guarded)

		// A partition: a file system that is not a pool (vdd, formatted
		// here). Only it is offered, not the ESP, the root slots, /var or
		// the pool's disks; it mounts under its label's name, and is
		// remembered for the next boot.
		if s.has("a partition that is not a pool, mounted from the API", s.apiHas("storage", func(v any) bool { return hasKey(v, "partitions") })) {
			s.must(`mkfs.ext4 -q -L "Check Data" /dev/vdd`)
			var st any
			offered := func() bool {
				st = s.getJ("storage")
				var p []string
				for _, x := range list(st, "partitions") {
					p = append(p, sAt(x, "path"))
				}
				return strings.Join(p, " ") == "/dev/vdd"
			}
			s.until(30*time.Second, offered)
			var got []string
			for _, x := range list(st, "partitions") {
				got = append(got, fmt.Sprintf("%s %s %s %s", sAt(x, "path"), sAt(x, "fstype"), sAt(x, "state"), sAt(x, "mount_name")))
			}
			if strings.Join(got, ", ") != "/dev/vdd ext4 unmounted check-data" {
				s.die("the partitions offered are not just vdd: %s", compact(at(st, "partitions")))
			}
			uuid := sAt(st, "partitions.0.uuid")
			part := "/var/mnt/check-data"
			s.ask("POST", "storage/partitions/"+uuid+"/mount", `{}`, "the partition's mount was refused")
			if m := s.out("findmnt -no FSTYPE,OPTIONS " + part); !grep(`^ext4 .*nosuid`, m) {
				s.die("%s is not mounted: %s", part, m)
			}
			if c := s.code("POST", "storage/partitions/"+uuid+"/mount", `{"name":"pool"}`); c != 409 {
				s.die("mounting a mounted partition answered %d", c)
			}
			s.ask("POST", "storage/partitions/"+uuid+"/unmount", nil, "the partition's unmount was refused")
			if !s.test("! touch " + part + "/x 2>/dev/null") {
				s.die("the unmounted %s can be written to", part)
			}
			s.ask("POST", "storage/partitions/"+uuid+"/mount", `{}`, "the partition's second mount was refused")
			s.must("echo kept > " + part + "/file")
			s.st.Part = part
			s.ok("partition vdd mounted at %s, the only one offered; unmounted, its directory takes no writes", part)
		}
	})

	s.Step(6, "a stack from the drafts template", func() {
		pool := s.st.Pool
		s.must("mkdir -p /var/lib/stack-src/site")
		// The template of the version that runs: a release reads only the
		// manifest versions it knew, so a release's comes from its tag.
		for _, f := range []string{"homelab-stack.json", "flake.nix"} {
			b, err := r.template(v, f)
			s.dieIf(err, "read the drafts template")
			s.putData("/var/lib/stack-src/site/"+f, strings.ReplaceAll(string(b), "NAME", "site"))
		}
		s.must("cd /var/lib/stack-src/site && git init -q && git add -A && git -c user.name=check -c user.email=check@example.com commit -qm site")
		rev := s.must("git -C /var/lib/stack-src/site rev-parse HEAD")
		s.st.Rev = rev
		s.ask("POST", "stacks", `{"name":"site","source":"git+file:///var/lib/stack-src/site"}`, "install refused")
		// The stack's page makes the folder: a subvolume on the pool, owned
		// by the host's ids for the user and group the template declares
		// (nginx, the first name, so 2000). A version from before that takes
		// a folder made by hand.
		madefolder := ""
		if s.has("a stack's folder made from the API", s.answers(200, "GET", "stacks-ids", nil)) {
			s.ask("POST", "stacks/site/variables/SITE_DIR/folder", map[string]string{"place": pool, "folder": "site"}, "the folder was not made")
			if o := s.out("stat -c %u:%g " + pool + "/site"); o != "2000:2000" {
				s.die("%s/site is owned by %s, not the stack ids 2000:2000", pool, o)
			}
			if !grep(`\bsite\b`, s.out("bcachefs subvolume list "+pool)) {
				s.die("%s/site is not a subvolume", pool)
			}
			sd := find(s.getJ("stacks/site"), "variables", "name", "SITE_DIR")
			if sAt(sd, "value")+" "+sAt(sd, "owner_state") != pool+"/site ok" {
				s.die("SITE_DIR is not the made folder on the stack ids")
			}
			madefolder = "; its folder a subvolume made from the API, owned by the stack ids"
		} else {
			s.must("mkdir -p " + pool + "/site && chown 2000:2000 " + pool + "/site")
			s.put("stacks/site/variables/SITE_DIR", map[string]string{"value": pool + "/site"}, "variable refused")
		}
		s.must("echo '<p>image-check</p>' > " + pool + "/site/index.html")
		// A folder bound on top of what the manifest asks for, read-only. A
		// version from before added folders has none.
		added := false
		if s.has("a host folder added to a stack", s.apiHas("stacks/site", func(v any) bool { return hasKey(v, "folders") })) {
			s.must("mkdir -p " + pool + "/library && echo shared > " + pool + "/library/file")
			s.put("stacks/site/folders", map[string]any{"folders": []any{map[string]any{"path": pool + "/library", "mount": "/library", "read_only": true}}},
				"the added folder was refused")
			added = true
		}
		s.ask("POST", "stacks/site/approve", map[string]string{"rev": rev}, "approve refused")
		s.ask("POST", "stacks/site/start", nil, "start refused")
		// Nix builds the stack: the machine is busy, and one poll that fails
		// is not the stack's failure.
		running := false
		for i := 0; i < 120 && !running; i++ {
			time.Sleep(5 * time.Second)
			st, v, err := s.stack()
			if err != nil {
				continue
			}
			running = st.Running
			if e := sAt(v, "operation.error"); !running && e != "" {
				s.die("stack failed: %s", e)
			}
		}
		if !running {
			s.die("the stack did not start in 10 minutes")
		}
		if !s.serves("site", "/", "image-check") {
			s.die("the stack's route is not served at site.%s", r.Domain)
		}
		refused := ""
		if s.has("the dashboard's sign-in", s.hasSignin) {
			s.stackRefused("site")
			refused = "; the dashboard refuses it"
		}
		if s.st.OnVar {
			st := s.getJ("stacks-store")
			if !(nAt(st, "total_bytes") > 0 && nAt(st, "free_bytes") > 0 && !bAt(st, "own_partition")) {
				s.die("the Nix store's disk is not reported: %s", compact(st))
			}
		}
		if madefolder != "" && !grep(`(?m)^nginx:x:2000:`, s.out("cat /var/lib/homelab/appdata/stacks/site/root/etc/passwd")) {
			s.die("nginx in the stack does not have the id 2000")
		}
		addedNote := ""
		if added {
			leader := s.out("machinectl show site -p Leader --value")
			// Through the leader's root: the stack's mounts, read-only flag
			// and all, without a binary of the host's inside the stack.
			if s.out("cat /proc/"+leader+"/root/library/file") != "shared" {
				s.die("the added folder is not at /library in the stack")
			}
			if !s.test("! touch /proc/" + leader + "/root/library/new 2>/dev/null") {
				s.die("the stack wrote to the read-only added folder")
			}
			if !s.test("grep -q ' /library ro,' /proc/" + leader + "/mountinfo") {
				s.die("/library is not mounted read-only in the stack")
			}
			addedNote = "; a host folder added read-only at /library"
		}
		s.ok("stack site built, serving the page from the pool at https://site.%s%s%s%s", r.Domain, refused, madefolder, addedNote)
		s.stackFork(rev)
		if !s.serves("site", "/", "image-check") {
			s.die("the stack's route is not served after the fork")
		}
		if s.stackFreeze() {
			s.ok("stack site frozen with its processes kept, its route silent and the API answering; thawed, the same processes serve it")
		}
		if s.stackShell() {
			s.ok("a root shell in stack site, from homelab stacks shell and over the daemon's WebSocket, follows a resize and ends with the shell")
		}
		s.stackLongName(pool)
		s.poolLifecycle()
	})

	s.Step(7, "host settings: VPN, tunnel, firewall, name and time zone", func() {
		peer := s.must("wg genkey | wg pubkey")
		s.put("host-settings/wireguard", map[string]any{"enabled": true, "address": "10.10.0.1/24", "endpoint": "vpn.example.net:51820",
			"peers": []any{map[string]any{"name": "laptop", "public_key": peer, "address": "10.10.0.2"}}}, "VPN refused")
		if s.has("a VPN device's keys made by the host", s.hasDeviceKeys()) {
			s.deviceKeys()
		}
		creds := `{"AccountTag":"acc","TunnelID":"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0","TunnelSecret":"c2VjcmV0"}`
		s.put("host-settings/tunnel", map[string]any{"enabled": true, "credentials": creds,
			"routes": []any{map[string]any{"hostname": "site.example.net", "target": "site"}}}, "tunnel refused")
		s.put("host-settings/firewall", `{"openings":[{"port":8443,"proto":"tcp","from":["10.0.2.0/24"]}]}`, "firewall refused")
		s.put("host-settings/host", `{"hostname":"checkhost","timezone":"Europe/Stockholm"}`, "host refused")
		var errs []string
		for _, e := range list(s.getJ("host-settings"), "applied.errors") {
			errs = append(errs, str(e))
		}
		if len(errs) > 0 {
			s.die("not applied: %s", strings.Join(errs, "; "))
		}
		time.Sleep(2 * time.Second)
		s.hostCheck()
		s.ok("VPN devices%s, tunnel route, firewall, host name and time zone in place", s.st.Made)
	})

	s.Step(8, "after a reboot", func() {
		froze := ""
		if s.has("freezing a stack", s.apiHas("stacks/site", func(v any) bool { return hasKey(v, "frozen") })) {
			s.post("stacks/site/freeze", `{}`, "the freeze before the reboot was refused")
			if c := s.call("POST", "system/power", `{"action":"reboot"}`).Problem(); c != "stacks_frozen" {
				s.die("a reboot from the API was not refused with the stack frozen")
			}
			froze = "; frozen before it, thawed by it and started at boot"
		}
		s.rebootVM()
		s.booted(v)
		sitewait = s.hostCheck()
		if s.st.Part != "" && s.out("cat "+s.st.Part+"/file") != "kept" {
			s.die("the partition is not mounted at %s after a reboot", s.st.Part)
		}
		if s.out("cat /etc/machine-id") != s.st.Installed {
			s.die("the machine ID changed over a reboot")
		}
		s.stackUp(0, "at the end of the step")
		if froze != "" {
			st := s.getJ("stacks/site")
			if sAt(st, "frozen")+" "+sAt(st, "autostart") != "false true" {
				s.die("the stack after the reboot: %s", pick(st, "frozen", "autostart"))
			}
		}
		s.ok("everything still in place, the machine ID too (the stack running %ds after the daemon answered%s)", int(sitewait.Seconds()), froze)
	})
}

func virtios(paths []string) []vmDisk {
	var ds []vmDisk
	for _, p := range paths {
		ds = append(ds, virtio(p))
	}
	return ds
}

func grepLines(pattern, s string) string {
	var out []string
	for _, l := range lines(s) {
		if strings.Contains(l, pattern) {
			out = append(out, l)
		}
	}
	return strings.Join(out, " ")
}

// template is a file of the drafts template as version v has it: from the
// tag v<v> when there is one (a release), else from the checkout.
func (r *Run) template(v, f string) ([]byte, error) {
	path := "apps/core/internal/drafts/template/" + f
	if exec.Command("git", "-C", r.Root, "rev-parse", "-q", "--verify", "refs/tags/v"+v).Run() == nil {
		return exec.Command("git", "-C", r.Root, "show", "v"+v+":"+path).Output()
	}
	return os.ReadFile(filepath.Join(r.Root, path))
}
