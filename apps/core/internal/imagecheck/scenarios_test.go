//go:build imagecheck

package imagecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/imagecheck/fixture"
	"homelab-ngx.com/core/internal/imagecheck/guest"
	"homelab-ngx.com/core/internal/imagecheck/vm"
)

// The steps, in five scenarios. A scenario starts from fresh disks or from
// the snapshot another one saved (its disks, powered off, and the firmware's
// variables), so that it can run alone, again, or beside another.
//
//	installer  1-3    the installer medium; saves installed
//	host       4-8    the installed disk; saves configured
//	updates    9-12   from configured: updates, the fall back, a build
//	beside     13     from installed: the free-space install alone
//	under      14-19  the version under test's own medium, fresh firmware
type scenario struct {
	name    string
	first   int    // its first step
	from    string // the snapshot it starts from
	saves   string // the snapshot it leaves
	run     func(s *S)
	leftOut func(r *Run) string // why nothing of it runs, or ""
}

const totalSteps = 19

var scenarios = []*scenario{
	{name: "installer", first: 1, saves: "installed", run: installerSteps},
	{name: "host", first: 4, from: "installed", saves: "configured", run: hostSteps},
	{name: "updates", first: 9, from: "configured", run: updateSteps, leftOut: func(r *Run) string {
		if r.Next == "" && !r.Build {
			r.P.Plain("image-check: scenario updates (steps 9 to 12) is left out: image/out has one version, and BUILD is not 1")
			return "one version"
		}
		return ""
	}},
	{name: "beside", first: 13, from: "installed", run: besideSteps},
	{name: "under", first: 14, run: underSteps},
}

func scenarioByName(n string) *scenario {
	for _, s := range scenarios {
		if s.name == n {
			return s
		}
	}
	return nil
}

func scenarioNames() []string {
	var ns []string
	for _, s := range scenarios {
		ns = append(ns, s.name)
	}
	return ns
}

// producer is the scenario that saves a snapshot.
func producer(snap string) *scenario {
	for _, s := range scenarios {
		if s.saves == snap {
			return s
		}
	}
	return nil
}

// State is what a scenario's steps learn and later steps, or the scenarios
// that start from its snapshot, need. It is saved with the snapshot.
type State struct {
	Medium     string `json:"medium"`      // the installer medium's disk UUID
	Installed  string `json:"installed"`   // the installed disk's machine ID
	Pool       string `json:"pool"`        // where the pool is mounted
	Part       string `json:"part"`        // where the partition is mounted, or ""
	OnVar      bool   `json:"onvar"`       // the Nix store is on /var
	VPNDevices int    `json:"vpn_devices"` // the VPN devices hostcheck expects
	Made       string `json:"made"`        // the note on the device the host made keys for
	Lifecycle  bool   `json:"lifecycle"`   // the second pool's lifecycle was checked
	Backups    bool   `json:"backups"`     // a backup's preview, stop and run were checked
	SMTPRelay  bool   `json:"smtp_relay"`  // the host SMTP relay was checked
	Rev        string `json:"rev"`         // the stack's first commit
	Region     uint64 `json:"region"`      // where step 2 installed
}

// S is one scenario's run: its machine, disks and state.
type S struct {
	r  *Run
	sc *scenario
	T  *testing.T // the scenario's test
	t  *testing.T // the running step's

	dir  string // its disks (the snapshot it saves, if it saves one)
	logs string // its logs
	st   State

	m       *vm.Machine
	g       *guest.Guest
	boots   int
	serial  string
	ready   int    // the machine's boot that last finished starting
	marked  int    // the boot under way at mark
	bootID  string // its boot id
	running string // the version that runs, for has
	step    int
	failed  bool
	token   string // the installer's
	rel     *fixture.Release
	id      string // the last machine ID ownid read
}

func (r *Run) runScenario(t *testing.T, sc *scenario) {
	s := &S{r: r, sc: sc, T: t, t: t, st: State{VPNDevices: 1}}
	s.logs = filepath.Join(r.VM, "logs", sc.name)
	if sc.saves != "" {
		s.dir = r.snapDir(sc.saves)
	} else {
		s.dir = filepath.Join(r.VM, "disks", sc.name)
	}
	for _, d := range []string{s.dir, s.logs} {
		_ = makeWritable(d)
		if err := os.RemoveAll(d); err != nil {
			t.Fatalf("%v", err)
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("%v", err)
		}
	}
	if sc.from != "" {
		m, err := r.readSnap(sc.from)
		if err != nil {
			s.fail("no snapshot %s to start from: %v", sc.from, err)
			t.FailNow()
		}
		s.st = m.State
		for _, id := range m.IDs {
			r.addID(id)
		}
		for _, k := range m.Skipped {
			r.skip(k)
		}
		if err := copyFile(filepath.Join(r.snapDir(sc.from), "vars.fd"), filepath.Join(s.dir, "vars.fd")); err != nil {
			s.fail("%v", err)
			t.FailNow()
		}
	}
	defer s.finish()
	sc.run(s)
	if s.failed || t.Failed() {
		return
	}
	s.stop()
	switch {
	case sc.saves != "":
		s.save()
	case !r.Keep && !r.Resume:
		// A scenario that passed needs none of its disks.
		r.roomUsed()
		_ = os.RemoveAll(s.dir)
	}
}

// fail prints a failure without ending anything.
func (s *S) fail(format string, args ...any) {
	s.r.P.Fail(s.sc.name, fmt.Sprintf(format, args...))
}

// die fails the step: what it says is why.
func (s *S) die(format string, args ...any) {
	s.t.Helper()
	s.fail(format, args...)
	s.t.FailNow()
}

// dieIf dies with err, when there is one.
func (s *S) dieIf(err error, format string, args ...any) {
	s.t.Helper()
	if err != nil {
		s.die("%s: %v", fmt.Sprintf(format, args...), err)
	}
}

func (s *S) ok(format string, args ...any) { s.r.P.OK(s.sc.name, fmt.Sprintf(format, args...)) }
func (s *S) note(format string, args ...any) {
	s.r.P.Note(s.sc.name, fmt.Sprintf(format, args...))
}

// Step runs step n; after a step that failed the scenario's later ones do
// not run.
func (s *S) Step(n int, title string, fn func()) {
	if s.failed {
		return
	}
	s.step = n
	s.r.roomUsed()
	s.r.P.Step(s.sc.name, n, title)
	start := time.Now()
	ok := s.T.Run(fmt.Sprintf("step%02d", n), func(t *testing.T) {
		s.t = t
		defer func() { s.t = s.T }()
		fn()
	})
	s.r.P.Took(s.sc.name, n, time.Since(start))
	if !ok {
		s.failed = true
	}
}

// finish: however the scenario ends. A failed one keeps the machine's
// evidence first, and with KEEP leaves it running.
func (s *S) finish() {
	if s.rel != nil {
		s.rel.Close()
	}
	if !s.failed && !s.T.Failed() {
		s.stop()
		return
	}
	s.evidence()
	if s.r.Keep && s.m != nil && s.m.Alive() {
		s.m.Detach()
		_ = os.WriteFile(filepath.Join(s.dir, "qemu.pid"), []byte(fmt.Sprint(s.m.Pid())), 0o644)
		s.note("VM kept: ssh -i %s -p %d root@127.0.0.1", filepath.Join(s.r.VM, "key"), s.m.Config.SSHPort)
		return
	}
	s.stop()
}

// save freezes the scenario's disks as its snapshot: the machine is off.
func (s *S) save() {
	var disks []string
	ents, _ := os.ReadDir(s.dir)
	for _, e := range ents {
		disks = append(disks, e.Name())
		_ = os.Chmod(filepath.Join(s.dir, e.Name()), 0o444)
	}
	s.r.mu.Lock()
	ids := append([]string(nil), s.r.ids...)
	skipped := append([]string(nil), s.r.skipped...)
	s.r.mu.Unlock()
	m := snapMeta{Key: s.r.snapKey(s.sc.saves), Harness: s.r.Harness, State: s.st, IDs: ids, Skipped: skipped, Disks: disks}
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.Chmod(s.dir, 0o755)
	if err := os.WriteFile(filepath.Join(s.dir, "meta.json"), b, 0o644); err != nil {
		s.fail("could not save snapshot %s: %v", s.sc.saves, err)
		s.T.Fail()
	}
}

func makeWritable(dir string) error {
	return filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			_ = os.Chmod(p, info.Mode()|0o200)
		}
		return nil
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Disks.

// path is a disk of the scenario's.
func (s *S) path(name string) string { return filepath.Join(s.dir, name) }

// newDisk makes an empty disk of size bytes.
func (s *S) newDisk(name string, size int64) string {
	p := s.path(name + ".qcow2")
	s.dieIf(vm.QemuImg("create", "-q", "-f", "qcow2", p, fmt.Sprint(size)), "make disk %s", name)
	return p
}

// overlay is a disk whose reads come from base until it is written.
func (s *S) overlay(base, name string) string {
	p := s.path(name + ".qcow2")
	s.dieIf(vm.Overlay(base, p), "make disk %s", name)
	return p
}

// fromSnap is a disk of the snapshot the scenario started from, written
// through an overlay of its own.
func (s *S) fromSnap(file, name string) string {
	return s.overlay(filepath.Join(s.r.snapDir(s.sc.from), file), name)
}

// drop deletes disks no later step uses. The machine is off.
func (s *S) drop(paths ...string) {
	s.r.roomUsed()
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// Machines.

type bootOpts struct {
	order  bool // the firmware tries the disks in the order given
	noUUID bool // the machine has no firmware UUID
}

func virtio(p string) vm.Disk { return vm.Disk{Kind: vm.Virtio, Path: p} }
func usb(p string) vm.Disk    { return vm.Disk{Kind: vm.USB, Path: p} }

// boot starts the machine with disks and waits for it to answer.
func (s *S) boot(o bootOpts, disks ...vm.Disk) {
	s.t.Helper()
	if s.m != nil && s.m.Alive() {
		s.die("a machine runs already")
	}
	vars := s.path("vars.fd")
	if _, err := os.Stat(vars); err != nil {
		s.dieIf(copyFile(filepath.Join(s.r.OVMF, "OVMF_VARS.fd"), vars), "copy the firmware's variables")
	}
	s.boots++
	cfg := vm.Config{
		OVMFCode: filepath.Join(s.r.OVMF, "OVMF_CODE.fd"),
		Vars:     vars,
		CPUs:     s.r.CPUs,
		MemMiB:   s.r.MemMiB,
		Order:    o.order,
		Disks:    disks,
		Credentials: map[string][]byte{
			"ssh.authorized_keys.root": s.r.KeyPub,
			// Cap a stalled /dev/mapper/root wait in test VMs (still above loader-disk-wait's 30s udev wait).
			"systemd.unit-dropin.dev-mapper-root.device": []byte("[Unit]\nJobTimeoutSec=30s\n"),
		},
		SSHPort:  1, // picked at the start
		HTTPPort: 1,
		QMP:      filepath.Join(s.r.Tmp, s.sc.name+".qmp"),
	}
	if !o.noUUID {
		cfg.UUID = s.r.FWUUID
	}
	cfg.Serial = filepath.Join(s.logs, fmt.Sprintf("serial-%02d-step%d-%s.log", s.boots, s.step, cfg.First()))
	s.serial = cfg.Serial
	m, err := vm.Start(context.Background(), cfg, filepath.Join(s.logs, fmt.Sprintf("qemu-%02d.log", s.boots)), !s.r.NoVsock, s.r.Keep)
	s.dieIf(err, "start the VM")
	s.m = m
	_ = os.WriteFile(filepath.Join(s.dir, "qemu.pid"), []byte(fmt.Sprint(m.Pid())), 0o644)
	s.ready = 0
	if s.g != nil {
		s.g.Close()
	}
	s.g = guest.New(s.dialer(), s.r.Key)
	s.up(480 * time.Second)
}

// dialer reaches the machine's sshd: over vsock, or the forwarded port.
func (s *S) dialer() guest.Dialer {
	m := s.m
	return func(ctx context.Context) (net.Conn, error) {
		if m.Notifies() {
			return vm.DialVsock(m.CID(), 22, 10*time.Second)
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", m.Config.SSHPort))
	}
}

// gone: why a wait for the machine ended, for its message.
func (s *S) gone(why string) string {
	if s.m == nil || !s.m.Alive() {
		return "QEMU ended"
	}
	return why
}

// up waits for a boot after the last one that answered to finish starting
// and for its SSH.
func (s *S) up(limit time.Duration) {
	s.t.Helper()
	// A boot is up when its SSH answers, on a boot that began after the
	// last one that was (QEMU reports each reset). systemd's READY=1 only
	// wakes the wait: the initrd sends one too, also on a boot that then
	// fails and resets, and a release whose start-up reloads systemd
	// (0.3.2's installed host) may send none at all.
	after := s.ready
	end := time.Now().Add(limit)
	for {
		if n := s.m.Current(); n > after && s.g.OK(context.Background(), "true") {
			s.m.Note("boot %d answers SSH", n)
			s.ready = n
			return
		}
		if !s.m.Alive() {
			s.die("QEMU ended (%s; serial log %s)", s.m.Status(), s.serial)
		}
		if time.Now().After(end) {
			s.die("the VM did not answer SSH within %s (%s; serial log %s)", fmtDur(limit), s.m.Status(), s.serial)
		}
		// Two seconds, or less when the boot under way says it is ready.
		cur := s.m.Current()
		if _, ok := s.m.ReadyAfter(cur - 1); ok {
			time.Sleep(2 * time.Second)
		} else {
			ctx, cancel := ctxFor(2 * time.Second)
			_, _ = s.m.WaitReady(ctx, cur-1)
			cancel()
		}
	}
}

func fmtDur(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	return d.String()
}

// mark notes the running boot; newboot waits for another to answer. A
// reboot stops the stacks and unmounts the pools first, and logind lets
// that hold it for up to five minutes.
func (s *S) mark() {
	s.t.Helper()
	id, err := s.g.Out(context.Background(), "cat /proc/sys/kernel/random/boot_id")
	if err != nil {
		s.die("no boot id before the reboot (serial log %s)", s.serial)
	}
	s.bootID = id
	s.marked = s.m.Current()
	s.ready = s.marked
}

func (s *S) otherBoot() bool {
	id, err := s.g.Out(context.Background(), "cat /proc/sys/kernel/random/boot_id")
	return err == nil && id != "" && id != s.bootID
}

func (s *S) newboot(limit time.Duration) {
	s.t.Helper()
	s.g.Close()
	s.up(limit)
	if !s.otherBoot() {
		s.die("the VM answered on the boot it had before the reboot (serial log %s)", s.serial)
	}
}

// restart reboots the machine from inside and waits for the next boot.
func (s *S) restart(limit time.Duration) {
	s.t.Helper()
	s.mark()
	_, _ = s.g.Run(context.Background(), "systemctl reboot", nil)
	s.newboot(limit)
}

// rebootVM is a reboot after which the daemon answers.
func (s *S) rebootVM() {
	s.t.Helper()
	s.restart(20 * time.Minute)
	// The daemon applies the host settings and mounts the pool after start.
	if !s.daemon(240 * time.Second) {
		s.die("the daemon does not answer after the reboot")
	}
}

// stop turns the machine off: poweroff from inside, then QEMU's end.
func (s *S) stop() {
	if s.m == nil {
		return
	}
	if s.m.Alive() {
		if s.g != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, _ = s.g.Run(ctx, "systemctl poweroff", nil)
			cancel()
		}
		s.m.Stop(60 * time.Second)
	} else {
		s.m.Kill()
	}
	if s.g != nil {
		s.g.Close()
	}
	s.m = nil
	_ = os.Remove(filepath.Join(s.dir, "qemu.pid"))
}

// off waits for the machine to turn itself off.
func (s *S) off() {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if s.m.WaitExit(ctx) != nil {
		s.die("the VM did not turn off")
	}
	s.stop()
}

// evidence: what the machine can say about a step that failed, kept next
// to the serial logs.
func (s *S) evidence() {
	if s.m == nil || !s.m.Alive() || s.g == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if !s.g.OK(ctx, "true") {
		s.note("the VM does not answer SSH; its serial log is %s", s.serial)
		return
	}
	for f, cmd := range map[string]string{
		"failed-units.log":   "systemctl --failed --no-legend --plain; echo; systemctl list-jobs --no-legend",
		"failed-journal.log": "journalctl -b --no-pager -o short-precise -n 20000",
		"failed-daemon.log":  "journalctl -b -u homelab-daemon --no-pager -o cat -n 5000",
	} {
		r, _ := s.g.Run(ctx, cmd, nil)
		_ = os.WriteFile(filepath.Join(s.logs, f), []byte(r.Stdout+r.Stderr), 0o644)
	}
	s.note("the VM's journal, failed units and daemon log: %s", filepath.Join(s.logs, "failed-*.log"))
}

// until tries probe every 2 s until it holds; false when it has not within
// limit, or when the machine is gone. The probe may fail in any way while
// it is waited for.
func (s *S) until(limit time.Duration, probe func() bool) bool {
	end := time.Now().Add(limit)
	for {
		if probe() {
			return true
		}
		if time.Now().After(end) {
			return false
		}
		if s.m != nil && !s.m.Alive() {
			return false
		}
		time.Sleep(2 * time.Second)
	}
}

// Commands in the machine.

func bg() context.Context { return context.Background() }

// sh runs cmd in the machine; a failure to run it ends the step, its exit
// code is the caller's.
func (s *S) sh(cmd string) guest.Result {
	s.t.Helper()
	r, err := s.g.Run(bg(), cmd, nil)
	s.dieIf(err, "run %q in the VM", short(cmd))
	return r
}

// out is what cmd printed, without the last newline; "" when it failed.
func (s *S) out(cmd string) string {
	r, err := s.g.Run(bg(), cmd, nil)
	if err != nil {
		return ""
	}
	return strings.TrimRight(r.Stdout, "\n")
}

// must runs cmd and ends the step when it fails.
func (s *S) must(cmd string) string {
	s.t.Helper()
	r := s.sh(cmd)
	if r.Code != 0 {
		s.die("%q in the VM: exit %d: %s", short(cmd), r.Code, strings.TrimSpace(r.Stderr))
	}
	return strings.TrimRight(r.Stdout, "\n")
}

// test says whether cmd exits 0.
func (s *S) test(cmd string) bool { return s.g.OK(bg(), cmd) }

// probe is a yes or no from cmd's exit code, for has.
func (s *S) probe(cmd string) func() (bool, error) {
	return func() (bool, error) { return s.g.Probe(bg(), cmd) }
}

// put writes a file of the host's to path in the machine.
func (s *S) putFile(src, dst string) {
	s.t.Helper()
	f, err := os.Open(src)
	s.dieIf(err, "read %s", src)
	defer f.Close()
	s.dieIf(s.g.Put(bg(), dst, f), "copy %s to the VM", filepath.Base(src))
}

// putData writes data to path in the machine.
func (s *S) putData(dst string, data string) {
	s.t.Helper()
	s.dieIf(s.g.Put(bg(), dst, strings.NewReader(data)), "write %s in the VM", dst)
}

func short(cmd string) string {
	cmd = strings.Join(strings.Fields(cmd), " ")
	if len(cmd) > 60 {
		return cmd[:57] + "..."
	}
	return cmd
}

// The daemon's API.

// call sends a request to /api/PATH; no answer ends the step.
func (s *S) call(method, path string, body any, header ...string) guest.Response {
	s.t.Helper()
	r, err := s.g.API(bg(), method, path, body, header...)
	s.dieIf(err, "%s /api/%s", method, path)
	return r
}

// try is call without ending the step: no answer is an error.
func (s *S) try(method, path string, body any, header ...string) (guest.Response, error) {
	return s.g.API(bg(), method, path, body, header...)
}

// get is the answer to GET /api/PATH; a status other than 2xx ends the
// step.
func (s *S) get(path string) []byte {
	s.t.Helper()
	r := s.call(http.MethodGet, path, nil)
	if r.Status/100 != 2 {
		s.die("GET /api/%s: %s", path, r)
	}
	return r.Body
}

// getJ is GET /api/PATH, parsed.
func (s *S) getJ(path string) any { return parse(s.get(path)) }

// decode is GET /api/PATH into v.
func (s *S) decode(path string, v any) {
	s.t.Helper()
	b := s.get(path)
	if err := json.Unmarshal(b, v); err != nil {
		s.die("GET /api/%s: %v: %.300s", path, err, b)
	}
}

// ask sends a request whose answer nothing reads: a failure prints the
// answer and dies with why.
func (s *S) ask(method, path string, body any, why string, header ...string) {
	s.t.Helper()
	r := s.call(method, path, body, header...)
	if r.Status/100 != 2 {
		s.die("%s: %s", why, r)
	}
}

// post is POST /api/PATH; a failure dies with why.
func (s *S) post(path string, body any, why string, header ...string) any {
	s.t.Helper()
	r := s.call(http.MethodPost, path, body, header...)
	if r.Status/100 != 2 {
		s.die("%s: %s", why, r)
	}
	return parse(r.Body)
}

// put is PUT /api/PATH; a failure dies with why.
func (s *S) put(path string, body any, why string) {
	s.t.Helper()
	s.ask(http.MethodPut, path, body, why)
}

// code is the status an API request answers with.
func (s *S) code(method, path string, body any) int {
	s.t.Helper()
	return s.call(method, path, body).Status
}

// daemon waits for the daemon's API.
func (s *S) daemon(limit time.Duration) bool {
	return s.until(limit, func() bool {
		r, err := s.try(http.MethodGet, "version", nil)
		return err == nil && r.Status == http.StatusOK
	})
}

// has: whether the running version has a feature. A release from before it
// has not, and the part of the step that checks it is left out; that is
// noted for the last line. The version under test has every feature, so
// there a no fails the run. A probe that cannot answer fails it too: a part
// must not be left out because a probe broke.
func (s *S) has(what string, probe func() (bool, error)) bool {
	s.t.Helper()
	yes, err := probe()
	if err != nil {
		s.die("could not tell whether %s has %s: %v", s.running, what, err)
	}
	if yes {
		return true
	}
	if s.running == s.r.Latest {
		s.die("%s has no %s, and it is the version under test", s.running, what)
	}
	if s.r.skip(what + " (" + s.running + ")") {
		s.note("- not checked on %s, which is from before it: %s", s.running, what)
	}
	return false
}

// apiHas is a probe for has: pred holds for GET /api/PATH.
func (s *S) apiHas(path string, pred func(v any) bool) func() (bool, error) {
	return func() (bool, error) {
		r, err := s.try(http.MethodGet, path, nil)
		if err != nil {
			return false, err
		}
		if r.Status/100 != 2 {
			return false, fmt.Errorf("GET /api/%s: %s", path, r)
		}
		return pred(parse(r.Body)), nil
	}
}

// answers is a probe for has: the API answers with status. A version
// without the route (404, 405) has not got the feature.
func (s *S) answers(status int, method, path string, body any) func() (bool, error) {
	return func() (bool, error) {
		r, err := s.try(method, path, body)
		if err != nil {
			return false, err
		}
		switch r.Status {
		case status:
			return true, nil
		case 404, 405:
			return false, nil
		}
		return false, fmt.Errorf("%s /api/%s: %s", method, path, r)
	}
}

// A predicate that is always the same answer, for has.
func yes(b bool) func() (bool, error) { return func() (bool, error) { return b, nil } }

var errNoAnswer = errors.New("no answer")

func ctxFor(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// key2: the second key, which only the installer's SSH keys setting lets
// in, opens a session as root. Over the network, as a person logs in: the
// sshd systemd starts on vsock reads the keys from its credential and root's
// home only.
func (s *S) key2() error {
	m := s.m
	g := guest.New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", m.Config.SSHPort))
	}, s.r.Key2)
	defer g.Close()
	_, err := g.Out(bg(), "true")
	return err
}
