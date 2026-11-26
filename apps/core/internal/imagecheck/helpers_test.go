//go:build imagecheck

package imagecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"homelab-ngx.com/core/internal/imagecheck/fixture"
	"homelab-ngx.com/core/internal/imagecheck/guest"
	"homelab-ngx.com/core/internal/installer"
	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/sysupdates"
)

// JSON, read loosely: what an older release answers need not have the
// fields HEAD's types have, and a field that is absent must not read as a
// zero.

func parse(b []byte) any {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	return v
}

// at is the value at a path of keys and indexes ("disks.0.name"); nil when
// any part is absent.
func at(v any, path string) any {
	if path == "" {
		return v
	}
	for _, k := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[k]
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

// str is a value as jq -r prints it.
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// s is the string at path, "" when absent.
func sAt(v any, path string) string {
	x := at(v, path)
	if x == nil {
		return ""
	}
	return str(x)
}

func bAt(v any, path string) bool { b, _ := at(v, path).(bool); return b }
func nAt(v any, path string) float64 {
	n, _ := at(v, path).(float64)
	return n
}

func hasKey(v any, key string) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, ok = m[key]
	return ok
}

func list(v any, path string) []any { l, _ := at(v, path).([]any); return l }

// find is the first element of the list at path whose key is val.
func find(v any, path, key, val string) any {
	for _, e := range list(v, path) {
		if sAt(e, key) == val {
			return e
		}
	}
	return nil
}

func compact(v any) string { b, _ := json.Marshal(v); return string(b) }

// pick is v with only the keys named, for a failure's message.
func pick(v any, keys ...string) string {
	m := map[string]any{}
	for _, k := range keys {
		m[k] = at(v, k)
	}
	return compact(m)
}

// grep says whether the pattern is in s.
func grep(pattern, s string) bool { return regexp.MustCompile(pattern).MatchString(s) }

func lines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func oneLine(s string) string { return strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", ";") }

// The steps' helpers.

// booted: the machine runs version (from here on s.running, which has asks
// about), its boot check passed, on its verity root.
func (s *S) booted(version string) {
	s.t.Helper()
	got, err := s.g.Out(bg(), `. /etc/os-release && echo "$IMAGE_VERSION"`)
	if err != nil {
		s.die("could not read the booted version: %v", err)
	}
	if got != version {
		s.die("booted %s, not %s", got, version)
	}
	s.running = version
	// The boot check waits for the daemon (up to two minutes) before it
	// passes.
	if !s.until(240*time.Second, func() bool { return s.test("systemctl is-active -q boot-complete.target") }) {
		s.die("boot-complete.target not reached")
	}
	if !s.test("findmnt -no SOURCE / | grep -q /dev/mapper/root") {
		s.die("/ is not on the verity device")
	}
}

// nofailed: no unit has failed; else which, and what each last said.
func (s *S) nofailed(where string) {
	s.t.Helper()
	r, err := s.g.Run(bg(), "systemctl --failed --no-legend --plain", nil)
	if err != nil || r.Code != 0 {
		s.die("could not list the failed units%s", where)
	}
	if strings.TrimSpace(r.Stdout) == "" {
		return
	}
	s.note("%s", strings.TrimSpace(r.Stdout))
	more := s.out(`systemctl --failed --no-legend --plain | while read -r u _; do echo "== $u"; journalctl -b -u "$u" --no-pager -n 15 -o cat; done`)
	for _, l := range lines(more) {
		s.note("  %s", l)
	}
	s.die("failed units%s", where)
}

// consoleUsers: who is logged in on a console and on which (a login
// session, not SSH), a line each: "root ttyS0" on the serial port; the
// screen's is tty1, or a pty under kmscon.
func (s *S) consoleUsers() (string, error) {
	return s.g.Out(bg(), `for s in $(loginctl list-sessions --no-legend | awk '{print $1}'); do
  [ "$(loginctl show-session "$s" -p Service --value)" = login ] &&
    echo "$(loginctl show-session "$s" -p Name --value) $(loginctl show-session "$s" -p TTY --value)"; done; true`)
}

// installedLogin: an installed host logs root in on its screen and serial
// port, as the stick does, and nothing else lets root in without a key: its
// password matches nothing and sshd takes public keys only. A release from
// before that asks for a login on the console, so nobody is logged in there.
func (s *S) installedLogin(when string) {
	s.t.Helper()
	if !s.has("a console that logs root in on an installed host", s.probe("! grep -q /var/lib/homelab/installer /usr/lib/homelab/console-login")) {
		users, err := s.consoleUsers()
		if err != nil {
			s.die("could not list the logins on the consoles")
		}
		if users != "" {
			s.die("the installed host logs someone in on a console%s: %s", when, oneLine(users))
		}
		return
	}
	s.autologin()
	if pw := s.out("getent shadow root | cut -d: -f2"); pw != "*" {
		s.die("root has a password field of %q, not *", pw)
	}
	// sshd -T prints each setting once; OpenSSH 10 in CamelCase.
	cfg := s.out("sshd -T")
	for _, want := range []string{"AuthenticationMethods publickey", "PasswordAuthentication no", "KbdInteractiveAuthentication no", "PermitRootLogin (prohibit|without)-password"} {
		if !grep(`(?mi)^`+want+`$`, cfg) {
			s.die("sshd does not have %q", want)
		}
	}
}

// autologin: root is logged in on the screen and on the serial port.
func (s *S) autologin() {
	s.t.Helper()
	both := func() bool {
		users, err := s.consoleUsers()
		if err != nil {
			return false
		}
		serial, screen := false, false
		for _, l := range lines(users) {
			f := strings.Fields(l)
			if len(f) == 2 && f[0] == "root" {
				if f[1] == "ttyS0" {
					serial = true
				} else {
					screen = true
				}
			}
		}
		return serial && screen
	}
	if !s.until(60*time.Second, both) {
		users, _ := s.consoleUsers()
		s.die("root is not logged in on both the screen and the serial port: %s", oneLine(users))
	}
}

// overhttp: the dashboard's page answers on the host's port 80.
func (s *S) overhttp() {
	s.t.Helper()
	c := &http.Client{Timeout: 30 * time.Second}
	r, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/", s.m.Config.HTTPPort))
	if err != nil {
		s.die("the dashboard does not answer over HTTP: %v", err)
	}
	defer r.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(r.Body)
	if r.StatusCode != 200 || !strings.Contains(b.String(), "<html") {
		s.die("the dashboard does not answer over HTTP: HTTP %d", r.StatusCode)
	}
}

// installerState: the installer's state, once its API answers.
func (s *S) installerState() (installer.InstallerState, any) {
	s.t.Helper()
	ok := s.until(120*time.Second, func() bool {
		r, err := s.try(http.MethodGet, "installer", nil)
		return err == nil && r.Status == 200
	})
	if !ok {
		s.die("the installer does not answer")
	}
	b := s.get("installer")
	var st installer.InstallerState
	if err := json.Unmarshal(b, &st); err != nil {
		s.die("the installer's state: %v", err)
	}
	return st, parse(b)
}

func diskNamed(st installer.InstallerState, name string) *installer.InstallDisk {
	for i := range st.Disks {
		if st.Disks[i].Name == name {
			return &st.Disks[i]
		}
	}
	return nil
}

// stickName: the installer medium's kernel name in the machine (vda as a
// virtio disk, sda as a USB stick), found by its disk UUID.
func (s *S) stickName() string {
	for _, l := range lines(s.out("lsblk -dno NAME,PTUUID")) {
		f := strings.Fields(l)
		if len(f) == 2 && strings.EqualFold(f[1], s.st.Medium) {
			return f[0]
		}
	}
	return ""
}

// runsFrom: the running system is all disk's: its /var, its /nix (a folder
// of /var) and both halves of its verity root (data and hash), started by
// the boot loader on loader (disk when ""). Disks by kernel name. The
// initrd's two services never fail a boot; what they could not do is in the
// kernel log, and nothing may be.
func (s *S) runsFrom(disk, loader string) {
	s.t.Helper()
	if loader == "" {
		loader = disk
	}
	want := fmt.Sprintf("loader=%s var=%s nix=%s root=%s %s", loader, disk, disk, disk, disk)
	if got := s.out(`dmesg | grep -E "homelab-(loader-disk|machine-id):"; true`); got != "" {
		s.die("the initrd could not do its part: %s", got)
	}
	r, err := s.g.Run(bg(), "bash -s", strings.NewReader(runsFromScript))
	if err != nil || r.Code != 0 {
		s.die("could not tell which disk the system runs from: %v %s", err, strings.TrimSpace(r.Stderr))
	}
	if got := strings.TrimSpace(r.Stdout); got != want {
		s.die("the system does not run from one disk: %s (expected %s)", got, want)
	}
}

const runsFromScript = `set -eu
disk() { lsblk -ndo PKNAME "$1"; }
uuid=$(tail -c +5 /sys/firmware/efi/efivars/LoaderDevicePartUUID-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f | tr -d '\000' | tr A-F a-f)
# Every partition with the loader's UUID: more than one disk shows here.
loader=$(lsblk -lno PKNAME,PARTUUID | awk -v u="$uuid" '$2 == u { print $1 }' | tr '\n' ' ')
dm=$(basename "$(readlink -f /dev/mapper/root)")
root=$(for s in /sys/block/"$dm"/slaves/*; do disk "/dev/${s##*/}"; done | tr '\n' ' ')
var=$(disk "$(findmnt -no SOURCE -M /var | tail -1)")
nix=$(disk "$(findmnt -no SOURCE -M /nix | tail -1 | sed 's/\[.*//')")
echo "loader=${loader% } var=$var nix=$nix root=${root% }"
`

var machineID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ownID: the installed host's machine ID (in s.id) is the one kept on its
// /var, not the firmware's UUID, and no other install's of this run.
func (s *S) ownID() {
	s.t.Helper()
	id := s.out("cat /etc/machine-id")
	if !machineID.MatchString(id) {
		s.die("the machine ID is not one: %s", id)
	}
	if v := s.out("cat /var/lib/homelab/machine-id"); v != id {
		s.die("the machine ID %s is not the one on /var", id)
	}
	if src := s.out("findmnt -no SOURCE /etc/machine-id"); !grep(`\[/lib/homelab/machine-id\]$`, src) {
		s.die("/etc/machine-id is not /var's file bound over it: %s", src)
	}
	if id == strings.ReplaceAll(s.r.FWUUID, "-", "") {
		s.die("the machine ID is the firmware's UUID")
	}
	if !s.r.addID(id) {
		s.die("another install has the machine ID %s too", id)
	}
	s.id = id
}

// spare: the disks a pool can be made from, as "path:contents" words.
func (s *S) spare() string {
	var w []string
	for _, d := range list(s.getJ("storage"), "unassigned") {
		w = append(w, sAt(d, "path")+":"+sAt(d, "contents"))
	}
	return strings.Join(w, " ")
}

func (s *S) pool(uuid string) any { return find(s.getJ("storage"), "pools", "uuid", uuid) }

// poolLifecycle: a second pool on vde, vdf and vdg, made, grown, shrunk and
// deleted through the daemon. It runs once, on the first version that can:
// a release from before the daemon made pools lists no contents for a disk.
func (s *S) poolLifecycle() {
	s.t.Helper()
	if s.st.Lifecycle {
		return
	}
	if !s.has("pools made, changed and deleted by the daemon", s.apiHas("storage", func(v any) bool {
		u := list(v, "unassigned")
		return len(u) > 0 && hasKey(u[0], "contents")
	})) {
		return
	}
	if sp := s.spare(); sp != "/dev/vde: /dev/vdf: /dev/vdg:" {
		s.die("the spare disks are not vde, vdf and vdg, blank: %s", sp)
	}
	if s.code("POST", "storage/pools", `{"name":"scratch","disks":["/dev/vde"],"copies":2}`) != 409 {
		s.die("two copies on one disk were not refused")
	}
	if s.code("POST", "storage/pools", `{"name":"scratch","disks":["/dev/vda"],"copies":1}`) != 409 {
		s.die("a pool on the system disk was not refused")
	}
	st := s.post("storage/pools", `{"name":"scratch","disks":["/dev/vde","/dev/vdf"],"copies":2}`, "the pool was not made")
	uuid := sAt(st, "uuid")
	if got := fmt.Sprintf("%s %s %s %s %d", sAt(st, "state"), sAt(st, "mountdir"), sAt(st, "data_replicas"), sAt(st, "auto_mount"), len(list(st, "disks"))); got != "mounted /var/mnt/scratch 2 true 2" {
		s.die("the new pool is not mounted at /var/mnt/scratch with 2 copies on 2 disks: %s", compact(st))
	}
	if !strings.Contains(s.out("findmnt -no FSTYPE /var/mnt/scratch"), "bcachefs") {
		s.die("/var/mnt/scratch is not mounted")
	}

	// A disk with a file system on it is erased only with the pool's name.
	s.must("mkfs.ext4 -q -L Old /dev/vdg")
	if !s.until(30*time.Second, func() bool {
		return strings.Contains(" "+s.spare()+" ", ` /dev/vdg:ext4 "Old" `) || s.spare() == `/dev/vdg:ext4 "Old"`
	}) {
		s.die("vdg is not offered with its contents: %s", s.spare())
	}
	if s.code("POST", "storage/pools/"+uuid+"/disks", `{"disk":"/dev/vdg"}`) != 422 {
		s.die("a disk with contents was added without the pool's name")
	}
	if !strings.Contains(s.out("blkid -o value -s TYPE /dev/vdg"), "ext4") {
		s.die("the refused add touched vdg")
	}
	if s.code("POST", "storage/pools/"+uuid+"/disks", `{"disk":"/dev/vdg","confirm":"scratch"}`) != 204 {
		s.die("vdg was not added")
	}
	if n := len(list(s.pool(uuid), "disks")); n != 3 {
		s.die("the pool does not have 3 disks")
	}

	// Removing a disk moves its data to the others first.
	s.must("dd if=/dev/urandom of=/var/mnt/scratch/file bs=1M count=300 status=none && sync")
	sum := s.must("sha256sum < /var/mnt/scratch/file")
	if s.code("DELETE", "storage/pools/"+uuid+"/disks?disk=/dev/vde", nil) != 202 {
		s.die("the removal of vde did not start")
	}
	var op any
	if !s.until(300*time.Second, func() bool {
		r, err := s.try("GET", "storage", nil)
		if err != nil || r.Status != 200 {
			return false
		}
		op = at(find(parse(r.Body), "pools", "uuid", uuid), "operation")
		return at(op, "finished_at") != nil
	}) {
		s.die("the removal of vde did not end: %s", compact(op))
	}
	if sAt(op, "phase")+" "+sAt(op, "error") != "done " {
		s.die("the removal of vde did not end well: %s", compact(op))
	}
	var paths []string
	for _, d := range list(s.pool(uuid), "disks") {
		paths = append(paths, sAt(d, "path"))
	}
	if strings.Join(paths, " ") != "/dev/vdf /dev/vdg" {
		s.die("the pool's disks are not vdf and vdg after the removal")
	}
	if s.must("sha256sum < /var/mnt/scratch/file") != sum {
		s.die("the file changed when vde left")
	}
	if sp := s.spare(); sp != "/dev/vde:" {
		s.die("the removed vde is not a blank spare disk: %s", sp)
	}
	if !s.test("! bcachefs show-super /dev/vde >/dev/null 2>&1") {
		s.die("the removed vde still has a superblock")
	}
	if s.code("DELETE", "storage/pools/"+uuid+"/disks?disk=/dev/vdf", nil) != 409 {
		s.die("a pool with two copies gave up its second disk")
	}

	// A pool a stack keeps a folder on is not deleted; this one is, by name.
	main := find(s.getJ("storage"), "pools", "mountdir", s.st.Pool)
	mainUUID, mainName := sAt(main, "uuid"), sAt(main, "name")
	if s.code("DELETE", "storage/pools/"+mainUUID+"?confirm="+mainName, nil) != 409 {
		s.die("the pool the stack uses was not refused")
	}
	if s.code("DELETE", "storage/pools/"+uuid+"?confirm="+mainName, nil) != 422 {
		s.die("a delete under another pool's name was not refused")
	}
	if s.code("DELETE", "storage/pools/"+uuid+"?confirm=scratch", nil) != 204 {
		s.die("the pool was not deleted")
	}
	if !s.test("! findmnt /var/mnt/scratch >/dev/null && ! test -e /var/mnt/scratch") {
		s.die("/var/mnt/scratch is still there")
	}
	if sp := s.spare(); sp != "/dev/vde: /dev/vdf: /dev/vdg:" {
		s.die("the deleted pool's disks are not blank spare disks: %s", sp)
	}
	if !s.test("! bcachefs show-super /dev/vdf >/dev/null 2>&1") {
		s.die("a deleted pool's disk still has a superblock")
	}
	var dirs []string
	for _, p := range list(s.getJ("storage"), "pools") {
		dirs = append(dirs, sAt(p, "mountdir"))
	}
	if strings.Join(dirs, " ") != s.st.Pool {
		s.die("the pools left are not just %s", s.st.Pool)
	}
	if !strings.Contains(s.out("findmnt -no FSTYPE "+s.st.Pool), "bcachefs") {
		s.die("%s is not mounted any more", s.st.Pool)
	}
	s.st.Lifecycle = true
	s.ok("a pool made on vde and vdf, grown with vdg (erased by name), shrunk by vde with its file kept, and deleted; the stack's pool refused")
}

// deploy is an operation's record, read loosely.
type deploy = sysupdates.CoreDeploy

// op starts an operation and waits for its result (the daemon may restart
// on the way): polled every 5 s, up to wait.
// backupLifecycle: a backup of a folder to a folder that already holds
// files, previewed, stopped mid-run and run to its end, through the daemon
// and the real runner, rclone and systemd. It runs once, on the first version
// that can preview.
func (s *S) backupLifecycle() {
	s.t.Helper()
	if s.st.Backups {
		return
	}
	const src, dst = "/var/bkcheck/src", "/var/bkcheck/dst"
	// Not under /tmp or /var/tmp: the runner's unit has its own.
	s.must("rm -rf /var/bkcheck && mkdir -p " + src + "/sub " + dst + "/data && " +
		"echo same > " + src + "/same.txt && cp -p " + src + "/same.txt " + dst + "/data/ && " +
		"echo new > " + src + "/sub/new.txt && echo changed-here > " + src + "/chg.txt && echo old > " + dst + "/data/chg.txt && " +
		"echo extra > " + dst + "/data/extra.txt")
	d := s.post("backup-destinations", map[string]any{"name": "check-disk", "kind": "rclone", "delete_removed": true,
		"rclone": map[string]any{"config": "[disk]\ntype = local\n", "path": dst}}, "the destination was not made")
	destID := int(nAt(d, "destination.id"))
	b := s.post("backups", map[string]any{"name": "check", "destination_ids": []int{destID},
		"sources": []any{map[string]any{"kind": "path", "path": src, "remote_path": "data"}}}, "the backup was not made")
	id := int(nAt(b, "id"))
	bk := fmt.Sprintf("backups/%d", id)
	clean := func() {
		s.ask(http.MethodDelete, bk, nil, "the backup was not deleted")
		s.ask(http.MethodDelete, fmt.Sprintf("backup-destinations/%d", destID), nil, "the destination was not removed")
		s.must("rm -rf /var/bkcheck")
	}
	if !s.has("a backup that can be previewed and stopped", func() (bool, error) { return hasKey(b, "previewing"), nil }) {
		clean()
		return
	}
	unit := fmt.Sprintf("homelab-backup-%d.service", id)
	listing := func() string {
		return s.must("cd " + dst + " && find . -type f -printf '%p %s %T@\\n' | sort | sha256sum")
	}
	preview := func() any {
		s.ask(http.MethodPost, bk+"/preview", nil, "the preview did not start")
		var p any
		if !s.until(120*time.Second, func() bool {
			r, err := s.try(http.MethodGet, bk+"/preview", nil)
			if err != nil || r.Status != 200 {
				return false
			}
			p = parse(r.Body)
			return sAt(p, "result") != "running"
		}) {
			s.die("the preview did not end: %s", compact(p))
		}
		return p
	}
	counts := func(p any) string {
		it := find(p, "items", "kind", "folder")
		return fmt.Sprintf("%s new=%v changed=%v unchanged=%v extra=%v deletes=%v", sAt(p, "result"),
			nAt(it, "new"), nAt(it, "changed"), nAt(it, "unchanged"), nAt(it, "extra"), bAt(it, "deletes"))
	}

	// A preview says what a run would do and leaves the destination alone.
	before := listing()
	p := preview()
	if got := counts(p); got != "success new=1 changed=1 unchanged=1 extra=1 deletes=true" {
		s.die("the preview of a filled destination: %s: %s", got, compact(p))
	}
	if listing() != before {
		s.die("the preview changed the destination")
	}
	if n := len(list(s.getJ(bk+"/runs"), "")); n != 0 {
		s.die("the preview left %d runs in the backup's history", n)
	}

	// A run is stopped mid-file: it ends cancelled, its unit is gone, and no
	// part of the file shows at the destination.
	s.must("dd if=/dev/urandom of=" + src + "/big.bin bs=1M count=200 status=none")
	s.ask(http.MethodPatch, bk, map[string]any{"bwlimit": "2M"}, "the speed limit was refused")
	run := int(nAt(s.post(bk+"/run", nil, "the run did not start"), "run_id"))
	runPath := fmt.Sprintf("backup-runs/%d", run)
	if !s.until(60*time.Second, func() bool { return nAt(s.getJ(runPath), "progress.bytes_done") > 0 }) {
		s.die("the run never reported progress: %s", compact(s.getJ(runPath)))
	}
	if s.code(http.MethodGet, bk+"/preview", nil) != 404 {
		s.die("the backup still has its preview after a run started")
	}
	if c := s.code(http.MethodPost, bk+"/cancel", nil); c != 204 {
		s.die("the run was not stopped: HTTP %d", c)
	}
	r := s.getJ(runPath)
	if sAt(r, "result") != "cancelled" || sAt(find(r, "items", "kind", "folder"), "result") != "cancelled" {
		s.die("the stopped run is not cancelled: %s", compact(r))
	}
	if st := s.out("systemctl is-active " + unit); st == "active" || st == "deactivating" {
		s.die("%s is still %s after the stop", unit, st)
	}
	if got := s.out("ls -A " + dst + "/data | grep -c big.bin"); got != "0" {
		s.die("part of big.bin is at the destination after the stop: %s", s.out("ls -lA "+dst+"/data"))
	}
	if s.code(http.MethodPost, bk+"/cancel", nil) != 409 {
		s.die("stopping a backup that is not running was not refused")
	}

	// The run after it sends the rest, and a preview then finds nothing to do.
	s.ask(http.MethodPatch, bk, map[string]any{"bwlimit": ""}, "the speed limit was not lifted")
	run = int(nAt(s.post(bk+"/run", nil, "the second run did not start"), "run_id"))
	runPath = fmt.Sprintf("backup-runs/%d", run)
	if !s.until(180*time.Second, func() bool { return sAt(s.getJ(runPath), "result") != "running" }) {
		s.die("the second run did not end")
	}
	if r := s.getJ(runPath); sAt(r, "result") != "success" {
		s.die("the second run: %s", compact(r))
	}
	if s.out("cmp "+src+"/big.bin "+dst+"/data/big.bin && test ! -e "+dst+"/data/extra.txt && echo same") != "same" {
		s.die("the destination does not match the source after the run: %s", s.out("ls -lAR "+dst))
	}
	if got := counts(preview()); got != "success new=0 changed=0 unchanged=4 extra=0 deletes=true" {
		s.die("the preview after a run: %s", got)
	}
	clean()
	s.st.Backups = true
	s.ok("a backup previewed against a filled folder with nothing changed there, stopped mid-file with no partial file left, then run to the counts the preview gave")
}

func (s *S) op(path string, body any, wait time.Duration) deploy {
	s.t.Helper()
	d, err := s.tryOp(path, body, wait)
	if err != nil {
		s.die("%v", err)
	}
	return d
}

func (s *S) tryOp(path string, body any, wait time.Duration) (deploy, error) {
	r, err := s.try("POST", path, body)
	if err != nil {
		return deploy{}, fmt.Errorf("%s refused: %v", path, err)
	}
	if r.Status/100 != 2 {
		return deploy{}, fmt.Errorf("%s refused: %s", path, r)
	}
	id := at(parse(r.Body), "id")
	if id == nil {
		return deploy{}, fmt.Errorf("%s started no operation: %s", path, r)
	}
	end := time.Now().Add(wait)
	for time.Now().Before(end) {
		time.Sleep(5 * time.Second)
		r, err := s.try("GET", "system-updates/deploys", nil)
		if err != nil || r.Status != 200 {
			continue
		}
		var ds []deploy
		if json.Unmarshal(r.Body, &ds) != nil {
			continue
		}
		for _, d := range ds {
			if float64(d.Id) == id.(float64) && d.Result != "running" {
				return d, nil
			}
		}
	}
	return deploy{}, fmt.Errorf("%s did not finish", path)
}

func deployString(d deploy) string { return compact(d) }

// web is a browser of the machine's own HTTPS.
func (s *S) https(name, path string) (guest.Response, error) {
	return s.g.Web().Get(bg(), name+"."+s.r.Domain, path)
}

// serves: GET https://NAME.domain/PATH answers 200 with text in it.
func (s *S) serves(name, path, text string) bool {
	r, err := s.https(name, path)
	return err == nil && r.Status == 200 && strings.Contains(string(r.Body), text)
}

// apicode is the status the dashboard's API answers with through Caddy at
// homelab.domain; 0 for none.
func (s *S) apicode(path string) int {
	r, err := s.https("homelab", path)
	if err != nil {
		return 0
	}
	return r.Status
}

// served: the dashboard's API answers through Caddy: 200, or 401 on a
// version with the sign-in.
func (s *S) served() bool { c := s.apicode("/api/version"); return c == 200 || c == 401 }

// hasSignin is a probe for has: no status at all is no answer.
func (s *S) hasSignin() (bool, error) {
	switch s.apicode("/api/auth") {
	case 200:
		return true, nil
	case 0:
		return false, fmt.Errorf("no answer from https://homelab.%s/api/auth", s.r.Domain)
	}
	return false, nil
}

// signin: through Caddy the dashboard's API wants a session; the first
// visitor sets the password, a second cannot, and the CLI can remove it.
func (s *S) signin() {
	s.t.Helper()
	host := "homelab." + s.r.Domain
	origin := "https://" + host
	pw := `{"password":"image-check-pw"}`
	codeOf := func(w *guest.Web, method, path string, body any, hdr ...string) string {
		r, err := w.Do(bg(), method, host, path, body, hdr...)
		if err != nil {
			return ""
		}
		return r.Problem()
	}
	anon := s.g.Web()
	if codeOf(anon, "GET", "/api/stacks", nil) != "setup_required" {
		s.die("the API answers before a password is set")
	}
	user := s.g.Web()
	r, err := user.Do(bg(), "POST", host, "/api/auth/setup", pw, "Origin", origin)
	if err != nil || !bAt(parse(r.Body), "signed_in") {
		s.die("the first password was refused: %v %s", err, r)
	}
	if r, err := user.Get(bg(), host, "/api/stacks"); err != nil || r.Status != 200 {
		s.die("signed in, the API still refuses: %v %s", err, r)
	}
	if codeOf(s.g.Web(), "GET", "/api/stacks", nil) != "login_required" {
		s.die("the API answers without a session")
	}
	if codeOf(s.g.Web(), "POST", "/api/auth/setup", pw, "Origin", origin) != "password_set" {
		s.die("a second visitor could set the password")
	}
	if !strings.Contains(s.out("homelab auth status"), "has a password") {
		s.die("the CLI does not see the password")
	}
	if r := s.sh("homelab auth reset"); r.Code != 0 {
		s.die("the CLI could not remove the password")
	}
	if codeOf(user, "GET", "/api/stacks", nil) != "setup_required" {
		s.die("a session outlived the reset")
	}
}

// stackRefused: from inside the stack's network the dashboard is refused
// and the stack's own route is not. (A probe from the host's side of the
// veth would leave through lo and be masqueraded to 127.0.0.1.)
func (s *S) stackRefused(name string) {
	s.t.Helper()
	leader := s.out("machinectl show " + name + " -p Leader --value")
	host := ""
	for _, l := range lines(s.out("ip -4 -o addr show dev " + stacks.HostIface(name))) {
		f := strings.Fields(l)
		if len(f) >= 4 && strings.HasPrefix(f[3], "10.99.") {
			host = strings.Split(f[3], "/")[0]
			break
		}
	}
	if leader == "" || host == "" {
		s.die("no leader or host address for %s", name)
	}
	probe := func(site, path string) string {
		return s.out(fmt.Sprintf("nsenter -t %s -n curl -sk -o /dev/null -w '%%{http_code}' --max-time 10 --resolve %s.%s:443:%s https://%s.%s%s",
			leader, site, s.r.Domain, host, site, s.r.Domain, path))
	}
	if c := probe("homelab", "/api/auth"); c != "403" {
		s.die("the dashboard answered %s inside %s, not 403", c, name)
	}
	if c := probe(name, "/"); c != "200" {
		s.die("%s's own route answered %s inside %s, not 200", name, c, name)
	}
}

// fixedIDs: the image's own accounts keep their ids from one version to the
// next, since files in /var are owned by number
// (usr/lib/sysusers.d/homelab.conf). False on a version from before they
// were fixed.
func (s *S) fixedIDs() bool {
	s.t.Helper()
	if !s.has("fixed ids for the image's accounts", s.probe(`grep -qs "^u caddy 956 " /usr/lib/sysusers.d/homelab.conf || exit 1`)) {
		return false
	}
	got := s.out(`echo "$(id -u caddy):$(id -g caddy) $(getent group homelab-daemon | cut -d: -f3) $(id -u homelab-nixd)"`)
	if got != "956:956 957 955" {
		s.die("the image's accounts have other ids than the ones it fixes: caddy, homelab-daemon, homelab-nixd are %s", got)
	}
	return true
}

// storeOnVar: a probe for has: the version keeps the Nix store on /var.
// (Its generator came with the reason a 100 MB ESP is refused, which step 1
// tells by it.)
func (s *S) storeOnVar() func() (bool, error) {
	return s.probe("test -e /usr/lib/systemd/system-generators/homelab-nix")
}

// hasDeviceKeys: the version makes a VPN device's keys: it answers an add
// without a name with 422; an older release has no such route (404).
func (s *S) hasDeviceKeys() func() (bool, error) {
	return s.answers(422, "POST", "host-settings/wireguard/peers", `{}`)
}

var privateKey = regexp.MustCompile(`PrivateKey = ([^\n]+)`)

// deviceKeys: a VPN device whose keys the host makes. Its config holds the
// next address, the endpoint and the private key of the public key the VPN
// now has.
func (s *S) deviceKeys() {
	s.t.Helper()
	dev := s.post("host-settings/wireguard/peers", `{"name":"phone"}`, "adding a VPN device refused")
	cfg := sAt(dev, "config")
	if !strings.Contains(cfg, "Address = 10.10.0.3/32") || !strings.Contains(cfg, "Endpoint = vpn.example.net:51820") {
		s.die("the new device's config: %s", compact(dev))
	}
	m := privateKey.FindStringSubmatch(cfg)
	if m == nil {
		s.die("the new device's config has no private key")
	}
	r, err := s.g.Run(bg(), "wg pubkey", strings.NewReader(m[1]+"\n"))
	if err != nil || r.Code != 0 {
		s.die("wg pubkey: %v", err)
	}
	pub := strings.TrimSpace(r.Stdout)
	if find(dev, "settings.wireguard.peers", "public_key", pub) == nil {
		s.die("the new device's private key is not its peer's")
	}
	s.st.VPNDevices, s.st.Made = 2, " (one with keys the host made)"
}

// The stack after a boot, an update or a change to the network, checked
// from the inside out so that a failure names the layer that broke:
//   - the daemon has the stack running;
//   - the host end of its link, ve-site, has the gateway address (the
//     stack's own address with .1);
//   - nginx in the stack answers on the stack's address;
//   - the page comes through Caddy at its route.
//
// stackFault is the first layer that does not hold, or "". A version from
// before the daemon reported a stack's address is checked at its route
// only.
func (s *S) stackFault() string {
	r, err := s.try("GET", "stacks/site", nil)
	if err != nil || r.Status != 200 {
		return "the daemon does not answer for the stack"
	}
	v := parse(r.Body)
	if !bAt(v, "running") {
		return "the daemon does not have the stack running"
	}
	if addr := sAt(v, "address"); addr != "" {
		gw := addr[:strings.LastIndex(addr, ".")] + ".1"
		if !strings.Contains(s.out("ip -4 -o addr show dev ve-site"), " "+gw+"/") {
			return "ve-site does not have the host's address " + gw
		}
		if !strings.Contains(s.out("curl -sf -m 5 http://"+addr+":8080/"), "image-check") {
			return "the stack does not answer at " + addr + ":8080"
		}
	}
	if !s.serves("site", "/", "image-check") {
		return "the stack's route is not served at site." + s.r.Domain
	}
	return ""
}

// stackEvidence: what each layer looks like, for the log of a failed run.
func (s *S) stackEvidence() {
	s.note("the stack, layer by layer:")
	if r, err := s.try("GET", "stacks/site", nil); err == nil {
		s.note("  %s", pick(parse(r.Body), "running", "want_running", "generation", "address", "operation"))
	}
	for _, l := range lines(s.out(`ip -4 -o addr show dev ve-site
systemctl status homelab-stack-site.service --no-pager -n 0
journalctl -b -u systemd-networkd -o short-precise --no-pager | grep -iE "ve-site|reconfigur" | tail -15
journalctl -b -u homelab-daemon -o cat --no-pager | grep -E '"module":"(stacks|hostcfg)"' | grep -vF '"kind":"cmd"' | tail -15; true`)) {
		s.note("  %s", l)
	}
}

// stack is the stack as the daemon has it.
func (s *S) stack() (stacks.Stack, any, error) {
	var st stacks.Stack
	r, err := s.try("GET", "stacks/site", nil)
	if err != nil {
		return st, nil, err
	}
	if r.Status != 200 {
		return st, nil, fmt.Errorf("%s", r)
	}
	err = json.Unmarshal(r.Body, &st)
	return st, parse(r.Body), err
}

// stopped: the stack is down and its stop has finished. The daemon refuses
// a start (busy) until the stop's finished_at is set.
func (s *S) stopped() bool {
	st, _, err := s.stack()
	return err == nil && !st.Running && (st.Operation == nil || st.Operation.FinishedAt != nil)
}

// stackRunning: the daemon has the stack running within limit. After a
// boot it answers before it has started the stacks again, so this is the
// wait that may be long. It is how long that took.
func (s *S) stackRunning(limit time.Duration) time.Duration {
	s.t.Helper()
	start := time.Now()
	for {
		st, _, err := s.stack()
		if err == nil && st.Running && (st.Operation == nil || st.Operation.FinishedAt != nil) {
			return time.Since(start).Round(time.Second)
		}
		if err == nil && st.Operation != nil && st.Operation.Error != "" {
			s.stackEvidence()
			s.die("the stack failed to start: %s", st.Operation.Error)
		}
		if time.Since(start) > limit {
			s.stackEvidence()
			s.die("the daemon did not have the stack running within %d s", int(limit.Seconds()))
		}
		time.Sleep(2 * time.Second)
	}
}

// stackUp: every layer holds within limit. A running stack is reachable
// at once, so the limit is short.
func (s *S) stackUp(limit time.Duration, when string) {
	s.t.Helper()
	start := time.Now()
	for {
		fault := s.stackFault()
		if fault == "" {
			return
		}
		if time.Since(start) >= limit {
			s.stackEvidence()
			if when != "" {
				fault += " " + when
			}
			s.die("%s", fault)
		}
		time.Sleep(2 * time.Second)
	}
}

// stackFreeze: the stack frozen keeps its processes and stops answering;
// the daemon and its service list still answer at once; thawed, the same
// processes serve the page. False on a version without freezing.
func (s *S) stackFreeze() bool {
	s.t.Helper()
	if !s.has("freezing a stack", s.apiHas("stacks/site", func(v any) bool { return hasKey(v, "frozen") })) {
		return false
	}
	leader := s.out("machinectl show site -p Leader --value")
	st := s.post("stacks/site/freeze", `{}`, "the freeze was refused")
	if !bAt(st, "frozen") || !bAt(st, "running") {
		s.die("the stack is not frozen after a freeze: %s", pick(st, "frozen", "running"))
	}
	if s.out("systemctl show -p FreezerState --value site.scope") != "frozen" {
		s.die("the stack's scope is not frozen")
	}
	// No answer in 5 s, or Caddy's own error for a silent backend.
	ctx, cancel := context.WithTimeout(bg(), 5*time.Second)
	r, err := s.g.Web().Get(ctx, "site."+s.r.Domain, "/")
	cancel()
	if err == nil && r.Status/100 <= 3 && r.Status/100 >= 2 {
		s.die("the frozen stack still answers (%d)", r.Status)
	}
	t0 := time.Now()
	if !bAt(s.getJ("stacks/site"), "frozen") {
		s.die("the stack does not show as frozen")
	}
	if r, err := s.try("GET", "services", nil); err != nil || r.Status != 200 {
		s.die("the service list fails while a stack is frozen")
	}
	if d := time.Since(t0); d > 5*time.Second {
		s.die("the API took %ds with a frozen stack", int(d.Seconds()))
	}
	if s.code("POST", "system/power", `{"action":"reboot"}`) != 409 {
		s.die("a reboot was not refused with a frozen stack")
	}
	if bAt(s.post("stacks/site/thaw", `{}`, "the thaw was refused"), "frozen") {
		s.die("the stack is still frozen after a thaw")
	}
	if !s.serves("site", "/", "image-check") {
		s.die("the thawed stack does not serve its page")
	}
	if s.out("machinectl show site -p Leader --value") != leader {
		s.die("the stack restarted over the freeze")
	}
	return true
}

// stackFork: the stack moves onto a draft of its own name (a fork of its
// approved commit), a file saved in the draft arrives as an approved
// update, and the stack runs that commit after the update. A version from
// before forks has no such operation, and nothing is checked.
func (s *S) stackFork(rev string) {
	s.t.Helper()
	forks := func() (bool, error) {
		r, err := s.try("POST", "stacks/nope/fork", nil)
		if err != nil {
			return false, err
		}
		return r.Problem() == "unknown_stack", nil
	}
	if !s.has("a stack forked into a draft", forks) {
		return
	}
	d := s.post("stacks/site/fork", nil, "the fork was refused")
	if got := sAt(d, "forked_from") + " " + sAt(d, "forked_rev"); got != "git+file:///var/lib/stack-src/site "+rev {
		s.die("the draft does not record its origin: %s", pick(d, "forked_from", "forked_rev"))
	}
	r := s.call("PUT", "drafts/site", `{"changes":[{"path":"notes/fork.txt","content":"forked\n"}],"message":"Add a note"}`)
	if r.Status/100 != 2 {
		s.die("the save was refused: %s", r)
	}
	saved := sAt(parse(r.Body), "rev")
	st := s.getJ("stacks/site")
	if src := sAt(st, "source"); src != "git+file:///var/lib/homelab-daemon/drafts/site" {
		s.die("the stack's source is %s", src)
	}
	if a := sAt(st, "approved.rev"); a != saved {
		s.die("the saved commit %s is not approved: %s", saved, a)
	}
	s.ask("POST", "stacks/site/update", nil, "update refused")
	start := time.Now()
	for {
		st, v, err := s.stack()
		if err == nil {
			runs := false
			for _, g := range st.Generations {
				if g.Id == st.Generation && g.Rev == saved {
					runs = true
				}
			}
			if st.Running && st.Operation != nil && st.Operation.FinishedAt != nil && runs {
				break
			}
			if e := sAt(v, "operation.error"); e != "" {
				s.die("the update failed: %s", e)
			}
		}
		if time.Since(start) > 10*time.Minute {
			s.die("the stack did not run %s within 10 minutes", saved)
		}
		time.Sleep(5 * time.Second)
	}
	s.ok("stack site forked into a draft; a saved file arrived as an update and runs (commit %.12s)", saved)
}

// stackLongName: a stack name longer than the 12 characters its interface
// takes. The site's repo again, with its name and route host changed, under
// a name of 14 characters: the host side of its link is ve- and the first
// 12, nspawn makes the same name, and the stack serves its route. A name
// that agrees in those 12 is refused, site keeps ve-site, and the stack is
// removed again so the steps after see only site. A version from before
// long names refuses the name.
func (s *S) stackLongName(pool string) {
	s.t.Helper()
	const name, clash = "site-long-name", "site-long-namx"
	ifc := stacks.HostIface(name)
	dir := "/var/lib/stack-src/" + name
	s.must("rm -rf " + dir + " && cp -a /var/lib/stack-src/site " + dir + " && cd " + dir +
		` && sed -i 's/"site"/"` + name + `"/g' homelab-stack.json` +
		" && git -c user.name=check -c user.email=check@example.com commit -qam " + name)
	rev := s.must("git -C " + dir + " rev-parse HEAD")
	add := map[string]string{"name": name, "source": "git+file://" + dir}
	long := func() (bool, error) {
		r, err := s.try("POST", "stacks", add)
		if err != nil {
			return false, err
		}
		switch {
		case r.Status == 201:
			return true, nil
		case r.Problem() == "invalid_name":
			return false, nil
		}
		return false, fmt.Errorf("POST /api/stacks: %s", r)
	}
	if !s.has("a stack name longer than 12 characters", long) {
		return
	}
	s.put("stacks/"+name+"/variables/SITE_DIR", map[string]string{"value": pool + "/site"}, "variable refused")
	s.ask("POST", "stacks/"+name+"/approve", map[string]string{"rev": rev}, "approve refused")
	s.ask("POST", "stacks/"+name+"/start", nil, "start refused")
	addr := ""
	if !s.until(10*time.Minute, func() bool {
		v := s.getJ("stacks/" + name)
		if e := sAt(v, "operation.error"); e != "" {
			s.die("%s failed: %s", name, e)
		}
		addr = sAt(v, "address")
		return bAt(v, "running") && addr != "" && (at(v, "operation") == nil || at(v, "operation.finished_at") != nil)
	}) {
		s.die("%s did not start in 10 minutes", name)
	}
	gw := addr[:strings.LastIndex(addr, ".")] + ".1"
	if !strings.Contains(s.out("ip -4 -o addr show dev "+ifc), " "+gw+"/") {
		s.die("%s does not have the host's address %s: links %s", ifc, gw, s.out("ip -o link show type veth | cut -d: -f2"))
	}
	if !s.serves(name, "/", "image-check") {
		s.die("the route of %s is not served", name)
	}
	r := s.call("POST", "stacks", map[string]string{"name": clash, "source": "git+file://" + dir})
	if r.Status != 409 || r.Problem() != "iface_taken" {
		s.die("%s, which shares %s, was not refused: %s", clash, ifc, r)
	}
	if !s.test("ip link show dev ve-site") {
		s.die("site no longer has ve-site")
	}
	s.ask("POST", "stacks/"+name+"/stop", nil, "stop refused")
	if !s.until(2*time.Minute, func() bool {
		v := s.getJ("stacks/" + name)
		return !bAt(v, "running") && (at(v, "operation") == nil || at(v, "operation.finished_at") != nil)
	}) {
		s.die("%s did not stop", name)
	}
	s.ask("DELETE", "stacks/"+name, nil, "removal refused")
	s.ok("stack %s runs on %s and serves its route; %s is refused, site keeps ve-site", name, ifc, clash)
}

// stackRace: systemd-networkd reading its configuration again leaves the
// stack's link alone. The daemon gives ve-site its address, and networkd
// also has a file for ve-* links, so a reconfigure takes the address off:
// the first change to the host's settings after a boot does one. Checked
// with the stack running, and with reloads going on while it starts. Only
// on the version under test: a release before cannot be fixed by this run.
func (s *S) stackRace() {
	s.t.Helper()
	// What a change to the host settings runs. The first one after a boot
	// makes networkd configure every link again; later ones do not.
	if !s.test("systemd-confext refresh --always-refresh=yes && networkctl reload") {
		s.die("confext refresh and networkctl reload refused")
	}
	time.Sleep(3 * time.Second)
	s.stackUp(0, "after the host's settings were applied as a change does")
	if !s.test("networkctl reconfigure ve-site") {
		s.die("networkctl reconfigure refused")
	}
	time.Sleep(3 * time.Second)
	s.stackUp(0, "after systemd-networkd reconfigured the link")
	s.ask("POST", "stacks/site/stop", nil, "stop refused")
	if !s.until(180*time.Second, s.stopped) {
		s.die("the stack did not stop in 3 minutes")
	}
	racer := make(chan struct{})
	go func() {
		defer close(racer)
		_, _ = s.g.Run(bg(), "for _ in $(seq 1 60); do networkctl reload; networkctl reconfigure ve-site 2>/dev/null; sleep 0.5; done", nil)
	}()
	s.ask("POST", "stacks/site/start", nil, "start refused")
	s.stackRunning(300 * time.Second)
	<-racer
	time.Sleep(3 * time.Second)
	s.stackUp(0, "after systemd-networkd reloads during its start")
}

// hasSMTPRelay reports whether the running image includes opensmtpd and the
// daemon API exposes the smtp host-settings section.
func (s *S) hasSMTPRelay() func() (bool, error) {
	return func() (bool, error) {
		ok, err := s.probe("test -x /usr/bin/smtpd")()
		if err != nil || !ok {
			return ok, err
		}
		return s.apiHas("host-settings", func(v any) bool { return hasKey(v, "smtp") })()
	}
}

// testSMTPRelay checks outbound mail relay from the host and across a stack's
// veth boundary using a temporary socket-activated SMTP sink on 127.0.0.1:2525,
// then leaves the authenticated STARTTLS relay configuration active.
func (s *S) testSMTPRelay() {
	s.t.Helper()
	const sinkScript = `#!/bin/bash
set -u
printf '220 check-sink ESMTP\r\n'
in_data=0
msg=""
while IFS= read -r line; do
  line="${line%$'\r'}"
  if ((in_data)); then
    if [[ "$line" == "." ]]; then
      in_data=0
      printf '%s---\n' "$msg" >> /run/check-smtp-received.txt
      msg=""
      printf '250 OK\r\n'
    else
      msg+="${line}"$'\n'
    fi
    continue
  fi
  upper="${line^^}"
  case "$upper" in
    EHLO*|HELO*) printf '250 check-sink\r\n' ;;
    MAIL\ FROM:*|RCPT\ TO:*|RSET*|NOOP*) printf '250 OK\r\n' ;;
    DATA*) in_data=1; msg=""; printf '354 End data\r\n' ;;
    QUIT*) printf '221 Bye\r\n'; break ;;
    *) printf '250 OK\r\n' ;;
  esac
done
`
	s.putData("/run/check-smtp-sink.sh", sinkScript)
	s.putData("/run/systemd/system/check-smtp-sink.socket", "[Socket]\nListenStream=127.0.0.1:2525\nAccept=yes\nTriggerLimitIntervalSec=0\n")
	s.putData("/run/systemd/system/check-smtp-sink@.service", "[Unit]\nDescription=Temporary SMTP sink for imagecheck\nStartLimitIntervalSec=0\n\n[Service]\nType=simple\nStandardInput=socket\nStandardOutput=socket\nStandardError=journal\nExecStart=/bin/bash /run/check-smtp-sink.sh\n")
	s.must("rm -f /run/check-smtp-received.txt && systemctl daemon-reload && systemctl start check-smtp-sink.socket")
	cleanupSink := func() {
		s.must("systemctl stop check-smtp-sink.socket && rm -f /run/systemd/system/check-smtp-sink.socket '/run/systemd/system/check-smtp-sink@.service' /run/check-smtp-sink.sh /run/check-smtp-received.txt && systemctl daemon-reload")
	}

	s.put("host-settings/smtp", map[string]any{"enabled": true, "host": "127.0.0.1", "port": 2525, "tls": "none"}, "smtp plaintext refused")
	if errs := list(s.getJ("host-settings"), "applied.errors"); len(errs) > 0 {
		cleanupSink()
		s.die("smtp plaintext apply errors: %s", compact(errs))
	}
	if !s.test("systemctl is-active -q homelab-smtpd.service") {
		cleanupSink()
		s.die("homelab-smtpd.service is not active for local sink test")
	}

	s.must(`printf 'From: <host@example.test>\r\nTo: <dest@example.test>\r\nSubject: probe\r\n\r\nhost-relay-probe\r\n' | curl --silent --show-error --max-time 10 --url smtp://127.0.0.1:25 --mail-from host@example.test --mail-rcpt dest@example.test --upload-file -`)
	if !s.until(15*time.Second, func() bool { return s.test("grep -qF host-relay-probe /run/check-smtp-received.txt") }) {
		cleanupSink()
		s.die("host mail did not reach the temporary SMTP sink through homelab-smtpd")
	}

	leader := s.out("machinectl show site -p Leader --value")
	gw := ""
	for _, l := range lines(s.out("ip -4 -o addr show dev " + stacks.HostIface("site"))) {
		f := strings.Fields(l)
		if len(f) >= 4 && strings.HasPrefix(f[3], "10.99.") {
			gw = strings.Split(f[3], "/")[0]
			break
		}
	}
	if leader == "" || gw == "" {
		cleanupSink()
		s.die("no leader or gateway address for site stack")
	}
	sendFromSite := func(tag string, maxSec int, verbose bool) string {
		vflag := ""
		if verbose {
			vflag = "-v "
		}
		return fmt.Sprintf(`printf 'From: <stack@example.test>\r\nTo: <dest@example.test>\r\nSubject: stack\r\n\r\n%s\r\n' | nsenter -t %s -n curl %s--silent --show-error --max-time %d --url smtp://%s:25 --mail-from stack@example.test --mail-rcpt dest@example.test --upload-file -`, tag, leader, vflag, maxSec, gw)
	}
	if s.test(sendFromSite("blocked-probe", 5, false)) {
		cleanupSink()
		s.die("stack site reached host port 25 without an egress grant")
	}

	// Open port 25 temporarily on site's veth input hook (matching an egress grant to host:25).
	s.must(`nft insert rule inet homelab_stack_site input iifname "ve-site" tcp dport 25 accept`)
	r := s.sh(sendFromSite("stack-relay-probe", 15, true))
	if r.Code != 0 {
		s.note("smtp stack failure evidence:\n%s", s.out(
			`ss -tlnp 'sport = :25'; echo '=== RESOLV ==='; ls -la /etc/resolv.conf; cat /etc/resolv.conf; `+
				`echo '=== RESOLVECTL ==='; timeout 6 resolvectl query 10.99.0.2 2>&1 || true; `+
				`echo '=== SITE NFT ==='; nft -a list table inet homelab_stack_site; `+
				`echo '=== HOST NFT ==='; nft list table inet homelab_host; `+
				`echo '=== SMTPD CONF ==='; cat /var/lib/homelab/smtp/smtpd.conf; `+
				`echo '=== SMTPD LOG ==='; journalctl -b -u homelab-smtpd --no-pager -n 20`))
	}
	h := s.out(`nft -a list chain inet homelab_stack_site input | awk '/tcp dport 25 accept/ {print $NF; exit}'`)
	if h != "" {
		s.must("nft delete rule inet homelab_stack_site input handle " + h)
	}
	if r.Code != 0 {
		cleanupSink()
		s.die("stack site failed to send to %s:25 with egress permitted (exit %d): %s", gw, r.Code, strings.TrimSpace(r.Stderr))
	}
	if !s.until(15*time.Second, func() bool { return s.test("grep -qF stack-relay-probe /run/check-smtp-received.txt") }) {
		cleanupSink()
		s.die("stack mail did not reach the temporary SMTP sink through homelab-smtpd")
	}
	cleanupSink()

	s.put("host-settings/smtp", map[string]any{
		"enabled": true, "host": "smtp.example.net", "port": 587, "tls": "starttls",
		"username": "apikey", "password": "check-smtp-secret",
	}, "smtp relay refused")
	raw := string(s.get("host-settings"))
	if strings.Contains(raw, "check-smtp-secret") {
		s.die("GET /api/host-settings leaked the SMTP password")
	}
	if !bAt(parse([]byte(raw)), "smtp.password_set") {
		s.die("smtp.password_set is false after saving password")
	}
	if perm := s.out("stat -c '%a:%G' /var/lib/homelab/smtp/secrets"); perm != "640:smtpd" {
		s.die("/var/lib/homelab/smtp/secrets permissions = %s, want 640:smtpd", perm)
	}
}

// hostCheck: everything the host settings asked for is in place. It is how
// long the stack took to run.
func (s *S) hostCheck() time.Duration {
	s.t.Helper()
	if !s.served() {
		s.die("the dashboard is not served at homelab.%s", s.r.Domain)
	}
	wait := s.stackRunning(300 * time.Second)
	s.stackUp(20*time.Second, "")
	peers := lines(s.out("wg show wg-homelab peers"))
	if len(peers) != s.st.VPNDevices {
		s.die("the VPN does not have its %d devices: %d", s.st.VPNDevices, len(peers))
	}
	if !strings.Contains(s.out("ip -4 -o addr show wg-homelab"), "10.10.0.1/24") {
		s.die("the VPN interface has no address")
	}
	if !s.test(`grep -q "originServerName: site.` + s.r.Domain + `" /var/lib/homelab/cloudflared/config.yml`) {
		s.die("the tunnel route is not in cloudflared's config")
	}
	if !strings.Contains(s.out("systemctl is-enabled homelab-cloudflared"), "runtime") {
		s.die("the tunnel is not turned on")
	}
	if !strings.Contains(s.out("nft list table inet homelab_host"), "dport 8443 accept") {
		s.die("the firewall table is not loaded")
	}
	if h := s.out("cat /proc/sys/kernel/hostname"); h != "checkhost" {
		s.die("the host name is %s", h)
	}
	if !strings.Contains(s.out("readlink /etc/localtime"), "Europe/Stockholm") {
		s.die("the time zone is not set")
	}
	if s.has("host SMTP relay", s.hasSMTPRelay()) {
		if !strings.Contains(s.out("systemctl is-enabled homelab-smtpd"), "runtime") {
			s.die("homelab-smtpd is not runtime-enabled")
		}
		if !s.test("systemctl is-active -q homelab-smtpd.service") {
			s.die("homelab-smtpd.service is not running")
		}
	}
	if !bAt(s.getJ("stacks/site"), "running") {
		s.die("the stack is not running")
	}
	// Caddy's way to the daemon is web.sock, behind the sign-in. In the
	// daemon's group it could open the CLI's socket, which asks for none.
	if !s.test(`gid=$(getent group homelab-daemon | cut -d: -f3); pid=$(systemctl show caddy -p MainPID --value)
! id -nG caddy | grep -qw homelab-daemon && ! grep "^Groups:" /proc/$pid/status | grep -qw "$gid"`) {
		s.die("Caddy is in the homelab-daemon group")
	}
	return wait
}

// claim: take the installer (its token in s.token): the first to ask gets
// it; when this boot's installer is held already, with the console's code.
func (s *S) claim() {
	s.t.Helper()
	if r, err := s.try("POST", "installer/claim", nil); err == nil && r.Status/100 == 2 {
		if s.token = sAt(parse(r.Body), "token"); s.token != "" {
			return
		}
	}
	code := s.out("cat /run/homelab-installer/takeover-code")
	if r, err := s.try("POST", "installer/takeover", map[string]string{"code": code}); err == nil && r.Status/100 == 2 {
		if s.token = sAt(parse(r.Body), "token"); s.token != "" {
			return
		}
	}
	s.die("the installer could not be claimed or taken over")
}

type installArgs struct {
	disk     string
	erase    bool
	hostname string
	ipv4     string // JSON, or ""
	esp      string
	region   uint64
}

// installTo installs from the medium through the installer's API. The
// installer's token stays in s.token.
func (s *S) installTo(a installArgs) {
	s.t.Helper()
	s.claim()
	plan := map[string]any{"disk": a.disk, "erase": a.erase}
	if a.esp != "" {
		plan["esp"] = a.esp
	}
	if a.region != 0 {
		plan["region"] = a.region
	}
	p := s.post("installer/plan", plan, "no plan for "+a.disk)
	if !bAt(p, "ok") {
		s.die("no plan for %s: %s", a.disk, sAt(p, "reason"))
	}
	body := map[string]any{"disk": a.disk, "erase": a.erase, "hostname": a.hostname, "timezone": "Europe/Stockholm",
		"ssh_keys": []string{strings.TrimSpace(string(s.r.Key2Pub))}}
	if a.ipv4 != "" {
		body["ipv4"] = json.RawMessage(a.ipv4)
	}
	if a.esp != "" {
		body["esp"] = a.esp
	}
	if a.region != 0 {
		body["region"] = a.region
	}
	s.ask("POST", "installer/install", body, "install refused", "X-Installer-Token", s.token)
	// One poll that fails is not the install's end: the machine is busy
	// writing.
	step := ""
	for i := 0; i < 120; i++ {
		time.Sleep(5 * time.Second)
		r, err := s.try("GET", "installer", nil)
		if err != nil || r.Status != 200 {
			continue
		}
		if step = sAt(parse(r.Body), "step"); step != "installing" {
			break
		}
	}
	st, _ := s.installerState()
	if step != "installed" {
		log := st.Log
		if len(log) > 15 {
			log = log[len(log)-15:]
		}
		s.die("install onto %s: %s: %s\n%s", a.disk, step, st.Error, strings.Join(log, "\n"))
	}
	// Every install can be restarted into: the firmware has its boot entry,
	// and the installer put it first.
	if !st.CanRestart {
		log := st.Log
		if len(log) > 4 {
			log = log[len(log)-4:]
		}
		s.die("install onto %s: no boot entry found: %s", a.disk, strings.Join(log, "; "))
	}
}

// install: installTo, then turn the machine off from the page's button.
func (s *S) install(a installArgs) {
	s.t.Helper()
	s.installTo(a)
	_, _ = s.try("POST", "installer/poweroff", nil, "X-Installer-Token", s.token)
	s.off()
}

// espFiles: the other system's files on an ESP, as one line; read where
// the ESP is mounted, else from a mount of its own.
func (s *S) espFiles(dev string) string {
	return s.out(`d=$(findmnt -no TARGET ` + dev + ` | head -1); if [ -z "$d" ]; then mkdir -p /run/esp && mount -o ro ` + dev + ` /run/esp && d=/run/esp; fi
cat $d/EFI/Microsoft/Boot/bootmgfw.efi $d/EFI/BOOT/BOOTX64.EFI | tr '\n' ' '; [ $d != /run/esp ] || umount /run/esp`)
}

// inUKI: version v's initrd has the kernel module (its path under drivers/,
// without .ko). The initrd's modules are an uncompressed archive in the
// UKI, so a file's path can be read there. The path from lib/modules on is
// the file's own; from kernel/ on it is also a line of modules.dep, which
// lists every module of the kernel, in the initrd or not.
func (r *Run) inUKI(v, module string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.ukis[v]
	if !ok {
		var err error
		if b, err = os.ReadFile(filepath.Join(r.Out, "homelab_"+v+".efi")); err != nil {
			return false, err
		}
		if r.ukis == nil {
			r.ukis = map[string][]byte{}
		}
		r.ukis[v] = b
	}
	re := regexp.MustCompile(`lib/modules/[^/]*/kernel/drivers/` + regexp.QuoteMeta(module) + `\.ko`)
	return re.Match(b), nil
}

// partUUIDs: the UUIDs of a disk file's /var partition (and /nix, on a
// version that gave the store its own), sorted.
func (s *S) partUUIDs(disk string) string {
	s.t.Helper()
	t, err := fixture.ReadGPT(disk)
	s.dieIf(err, "read %s's partitions", filepath.Base(disk))
	var u []string
	for _, p := range t.Partitions {
		if p.Name == "var" || p.Name == "nix" {
			u = append(u, p.UUID)
		}
	}
	sortStrings(u)
	return strings.Join(u, " ")
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// stageImage: version v's image files in the host's update folder, as a
// build there would.
func (s *S) stageImage(v string) {
	s.t.Helper()
	files, _ := filepath.Glob(filepath.Join(s.r.Out, "homelab_"+v+".root*.raw"))
	files = append([]string{filepath.Join(s.r.Out, "homelab_"+v+".efi")}, files...)
	for _, f := range files {
		s.putFile(f, "/var/lib/homelab/updates/"+filepath.Base(f))
	}
}

// diskID is a disk file's GPT disk UUID, lower case.
func (s *S) diskID(path string) string {
	s.t.Helper()
	t, err := fixture.ReadGPT(path)
	s.dieIf(err, "read %s's partitions", filepath.Base(path))
	return t.DiskID
}

// stackShell opens a root shell in stack site, from the CLI and over the
// daemon's WebSocket, and checks that it runs in the container: /etc/NIXOS
// is the stack's, never the host's.
func (s *S) stackShell() bool {
	s.t.Helper()
	if !s.has("a stack shell", func() (bool, error) {
		r, err := s.try(http.MethodGet, "stacks/site/shell", nil)
		return err == nil && r.Status != http.StatusNotFound && r.Status != http.StatusMethodNotAllowed, err
	}) {
		return false
	}
	const probe = `test -e /etc/NIXOS && echo in-stack-$((6*7))`
	if r := s.sh(`homelab stacks shell site -- sh -c '` + probe + `' </dev/null`); !strings.Contains(r.Stdout, "in-stack-42") {
		s.die("homelab stacks shell did not run the command in the stack: exit %d, %q, %q", r.Code, r.Stdout, strings.TrimSpace(r.Stderr))
	}

	ctx, cancel := context.WithTimeout(bg(), 60*time.Second)
	defer cancel()
	conn, res, err := s.g.WebSocket(ctx, "stacks/site/shell")
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		s.die("the stack shell's WebSocket did not open (HTTP %d): %v", status, err)
	}
	defer conn.CloseNow()
	var seen strings.Builder
	// until reads the terminal until want has been printed.
	until := func(want string) {
		s.t.Helper()
		for !strings.Contains(seen.String(), want) {
			_, b, err := conn.Read(ctx)
			if err != nil {
				s.die("the stack shell ended before it printed %q: %v; it printed %q", want, err, seen.String())
			}
			seen.Write(b)
		}
	}
	send := func(typ websocket.MessageType, b string) {
		s.t.Helper()
		if err := conn.Write(ctx, typ, []byte(b)); err != nil {
			s.die("write to the stack shell: %v", err)
		}
	}
	send(websocket.MessageText, `{"type":"resize","cols":123,"rows":45}`)
	// What is typed before the shell starts is lost, as it would be on a
	// console: wait for the prompt (its title names user@host).
	until("root@site")
	send(websocket.MessageBinary, probe+"; stty size\r")
	until("in-stack-42")
	until("45 123")
	send(websocket.MessageBinary, "exit\r")
	for {
		_, _, err := conn.Read(ctx)
		if err == nil {
			continue
		}
		var ce websocket.CloseError
		if !errors.As(err, &ce) || ce.Reason != "The shell ended." {
			s.die("the stack shell did not end with the shell: %v", err)
		}
		break
	}
	return true
}
