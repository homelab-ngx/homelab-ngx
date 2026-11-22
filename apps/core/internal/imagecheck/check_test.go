//go:build imagecheck

// The image check: boot the image `make image` built (image/out) in virtual
// machines and check it end to end. `make image-check` runs it; see
// scenarios.go for what each part checks, and CONTRIBUTING.md for the
// knobs.
package imagecheck

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"homelab-ngx.com/core/internal/imagecheck/report"
	"homelab-ngx.com/core/internal/imagecheck/vm"
)

// Run is one run of the check: what every scenario shares.
type Run struct {
	Root, Out, VM string
	OVMF          string
	Tmp           string // short paths for sockets

	Versions              []string // in image/out, oldest first, the ones used
	Version, Next, Broken string   // installed first, updated to, installed broken
	Latest                string   // the version under test: the newest that boots
	Under                 string   // what steps 14 on install from their own stick

	CPUs, MemMiB        int
	Keep, Build, Resume bool
	NoVsock             bool
	Jobs                int
	Only                []string
	Key                 ssh.Signer
	KeyPub, Key2Pub     []byte
	Key2                ssh.Signer
	FWUUID              string
	Domain              string
	P                   *report.Printer
	Harness             string // the harness's source, as a git tree

	mu      sync.Mutex
	ukis    map[string][]byte // the versions' UKIs, read once
	ids     []string          // every install's machine ID
	skipped []string          // what was not checked, on which version
	peak    int64
}

const fwuuid = "9a0c1e2f-3b4d-4e5f-8a9b-0c1d2e3f4a5b"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// refuse ends the run before the first boot: something it needs is not there.
func refuse(t *testing.T, format string, args ...any) {
	t.Helper()
	fmt.Printf("image-check: %s\n", fmt.Sprintf(format, args...))
	t.FailNow()
}

func TestImage(t *testing.T) {
	r := &Run{FWUUID: fwuuid, Domain: "check.example.com"}
	r.Root = env("HOMELAB_ROOT", "")
	if r.Root == "" {
		wd, _ := os.Getwd()
		for d := wd; d != "/"; d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "image", "base")); err == nil {
				r.Root = d
				break
			}
		}
	}
	if r.Root == "" {
		refuse(t, "no repository root: set HOMELAB_ROOT")
	}
	r.Out = env("HOMELAB_IMAGE_OUT", filepath.Join(r.Root, "image", "out"))
	r.VM = filepath.Join(r.Out, "vm")
	r.OVMF = os.Getenv("HOMELAB_OVMF")
	r.Keep = os.Getenv("KEEP") == "1"
	r.Build = os.Getenv("BUILD") == "1"
	r.Resume = os.Getenv("RESUME") == "1"
	r.NoVsock = os.Getenv("NOVSOCK") == "1"
	r.CPUs, _ = strconv.Atoi(env("CPUS", "4"))
	defMem := "4096"
	if r.Build {
		// One Go compile on the host takes about 2.5 GB.
		defMem = "6144"
	}
	r.MemMiB, _ = strconv.Atoi(env("MEM", defMem))
	r.Jobs, _ = strconv.Atoi(env("JOBS", "1"))
	if r.Jobs < 1 {
		r.Jobs = 1
	}
	if o := os.Getenv("ONLY"); o != "" {
		r.Only = strings.FieldsFunc(o, func(c rune) bool { return c == ',' || c == ' ' })
		for _, n := range r.Only {
			if scenarioByName(n) == nil {
				refuse(t, "ONLY names %s, which is no scenario (%s)", n, strings.Join(scenarioNames(), ", "))
			}
		}
	}
	r.P = report.New(os.Stdout, totalSteps)
	r.P.Prefix = r.Jobs > 1

	r.pickVersions(t)
	r.preflight(t)
	r.lock(t)
	r.keys(t)
	r.Harness = harnessTree(r.Root)

	r.P.Plain(fmt.Sprintf("image-check: %s is installed%s%s; steps 14 on install %s",
		r.Version, ifs(r.Next != "", " and updated to "+r.Next), ifs(r.Broken != "", "; "+r.Broken+" is installed and broken"), r.Under))
	if r.Next == "" {
		r.P.Plain("image-check: steps 9 and 10 (the updates) are left out: they need a second version")
	}
	if r.Broken == "" {
		r.P.Plain("image-check: step 11 (the fall back) is left out: it needs a third version")
	}
	if !r.Build {
		r.P.Plain("image-check: step 12 (a build on the host) is left out: it needs BUILD=1")
	}
	transport := "vsock"
	if r.NoVsock {
		transport = "a forwarded port (NOVSOCK=1)"
	}
	r.P.Plain("image-check: SSH goes over " + transport)

	r.runScenarios(t)
}

func ifs(c bool, s string) string {
	if c {
		return s
	}
	return ""
}

// pickVersions takes the newest three versions in image/out, or the ones
// VERSIONS names: the oldest is installed, the next is updated to, and the
// third installed and broken.
func (r *Run) pickVersions(t *testing.T) {
	var vs []string
	if v := os.Getenv("VERSIONS"); v != "" {
		vs = strings.Fields(v)
		if len(vs) > 3 {
			refuse(t, "VERSIONS names more than three versions")
		}
		for _, v := range vs {
			if _, err := os.Stat(filepath.Join(r.Out, "homelab_"+v+".efi")); err != nil {
				refuse(t, "no image %s in %s", v, r.Out)
			}
		}
	} else {
		files, _ := filepath.Glob(filepath.Join(r.Out, "homelab_*.efi"))
		for _, f := range files {
			vs = append(vs, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "homelab_"), ".efi"))
		}
		sort.Slice(vs, func(i, j int) bool { return versionLess(vs[i], vs[j]) })
		if len(vs) > 3 {
			r.P.Plain("image-check: older in " + r.Out + " and not used: " + strings.Join(vs[:len(vs)-3], " "))
			vs = vs[len(vs)-3:]
		}
	}
	if len(vs) == 0 {
		refuse(t, "no image in %s: run make image first", r.Out)
	}
	r.Versions = vs
	r.Version = vs[0]
	if len(vs) >= 2 {
		r.Next = vs[1]
	}
	if len(vs) >= 3 {
		r.Broken = vs[2]
	}
	r.Latest = r.Version
	if r.Next != "" {
		r.Latest = r.Next
	}
	// Steps 14 on install from a stick of their own and judge that version:
	// the one under test where its installer medium was built, not the
	// release before it, which is published and cannot be fixed.
	r.Under = r.Version
	if r.Next != "" && exists(filepath.Join(r.Out, "homelab_"+r.Next+".raw")) {
		r.Under = r.Next
	}
}

// versionLess orders versions as sort -V does, for the ones the image has
// (numbers between dots).
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, errx := strconv.ParseUint(as[i], 10, 64)
		y, erry := strconv.ParseUint(bs[i], 10, 64)
		if errx == nil && erry == nil {
			if x != y {
				return x < y
			}
			continue
		}
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// preflight: what the run needs, before the first boot, so that no step
// stops for it half an hour in.
func (r *Run) preflight(t *testing.T) {
	var missing []string
	for _, tool := range []string{"qemu-system-x86_64", "qemu-img", "sfdisk", "mkfs.vfat", "mmd", "mcopy", "gpg", "fsck.erofs", "mkfs.erofs", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		refuse(t, "not on PATH: %s (make image-check runs this in the image shell)", strings.Join(missing, " "))
	}
	if !exists(filepath.Join(r.OVMF, "OVMF_CODE.fd")) || !exists(filepath.Join(r.OVMF, "OVMF_VARS.fd")) {
		refuse(t, "HOMELAB_OVMF does not name the firmware's folder (the image shell sets it)")
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err != nil {
		refuse(t, "no KVM: /dev/kvm cannot be opened")
	} else {
		f.Close()
	}
	if !r.NoVsock {
		if err := vm.VsockAvailable(); err != nil {
			refuse(t, "no vsock for the machines: /dev/vhost-vsock: %v (load vhost_vsock and let this user open it, or run with NOVSOCK=1)", err)
		}
	}
	needs := func(v string, pats ...string) {
		for _, p := range pats {
			m, _ := filepath.Glob(filepath.Join(r.Out, p))
			if len(m) == 0 {
				refuse(t, "image %s is incomplete in %s: no %s (run make image again)", v, r.Out, p)
			}
		}
	}
	needs(r.Version, "homelab_"+r.Version+".raw", "core/homelab-core_"+r.Version+".raw")
	if r.Next != "" {
		needs(r.Next, "homelab_"+r.Next+".efi", "homelab_"+r.Next+".root*.raw", "core/homelab-core_"+r.Next+".raw")
	}
	if r.Broken != "" {
		needs(r.Broken, "homelab_"+r.Broken+".efi", "homelab_"+r.Broken+".root*.raw", "core/homelab-core_"+r.Broken+".raw")
	}
	if r.Under != r.Version {
		needs(r.Under, "homelab_"+r.Under+".raw")
	}
}

// lock: one check at a time in image/out, since a second would delete the
// first one's disks. The lock is a file with the run's pid; the machines an
// earlier run left (KEEP, or a run that was killed) are stopped here, or
// they would keep their memory and the blocks of deleted disks.
func (r *Run) lock(t *testing.T) {
	_ = os.MkdirAll(r.Out, 0o755)
	lockf := filepath.Join(r.Out, ".check.lock")
	if b, err := os.ReadFile(lockf); err == nil {
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 && pid != os.Getpid() && alive(pid) {
			refuse(t, "another image check is running in %s (pid %d)", r.Out, pid)
		}
	}
	if err := os.WriteFile(lockf, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		refuse(t, "%v", err)
	}
	t.Cleanup(func() { os.Remove(lockf) })
	pids, _ := filepath.Glob(filepath.Join(r.VM, "*", "*", "qemu.pid"))
	for _, f := range pids {
		b, _ := os.ReadFile(f)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 && alive(pid) {
			if cmd, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); strings.Contains(string(cmd), r.VM+"/") {
				r.P.Plain(fmt.Sprintf("image-check: stopping the VM an earlier run left (pid %d)", pid))
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
				for i := 0; i < 40 && alive(pid); i++ {
					time.Sleep(250 * time.Millisecond)
				}
			}
		}
		os.Remove(f)
	}
	if !r.Resume {
		if err := os.RemoveAll(r.VM); err != nil {
			refuse(t, "%v", err)
		}
	}
	if err := os.MkdirAll(r.VM, 0o755); err != nil {
		refuse(t, "%v", err)
	}
	tmp, err := os.MkdirTemp("", "imagecheck")
	if err != nil {
		refuse(t, "%v", err)
	}
	r.Tmp = tmp
	t.Cleanup(func() { os.RemoveAll(tmp) })
}

func alive(pid int) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

// keys: root's key, which the machines get as a credential, and a second
// one only the installer's SSH keys setting lets in.
func (r *Run) keys(t *testing.T) {
	mk := func(name string) (ssh.Signer, []byte) {
		// A resumed run keeps the keys: its snapshots' installs were given
		// them.
		if r.Resume {
			b, err := os.ReadFile(filepath.Join(r.VM, name))
			pub, perr := os.ReadFile(filepath.Join(r.VM, name+".pub"))
			if err == nil && perr == nil {
				if s, err := ssh.ParsePrivateKey(b); err == nil {
					return s, pub
				}
			}
		}
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			refuse(t, "%v", err)
		}
		s, err := ssh.NewSignerFromKey(priv)
		if err != nil {
			refuse(t, "%v", err)
		}
		pub := ssh.MarshalAuthorizedKey(s.PublicKey())
		pub = append(pub[:len(pub)-1], []byte(" "+name+"\n")...)
		// Kept for a person who wants into a machine KEEP left running.
		block, err := ssh.MarshalPrivateKey(priv, name)
		if err == nil {
			_ = os.WriteFile(filepath.Join(r.VM, name), pem.EncodeToMemory(block), 0o600)
			_ = os.WriteFile(filepath.Join(r.VM, name+".pub"), pub, 0o644)
		}
		return s, pub
	}
	r.Key, r.KeyPub = mk("key")
	r.Key2, r.Key2Pub = mk("key2")
}

// harnessTree is the git tree of the harness's source, recorded with each
// snapshot: a snapshot made by other code may hold other state.
func harnessTree(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD:apps/core/internal/imagecheck").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// imageKey identifies the image files of the versions a scenario chain
// uses: a new build of any of them makes a snapshot of them useless.
func (r *Run) imageKey() string {
	h := sha256.New()
	for _, v := range r.Versions {
		fmt.Fprintf(h, "%s\n", v)
		for _, f := range []string{"homelab_" + v + ".efi", "homelab_" + v + ".raw", "core/homelab-core_" + v + ".raw"} {
			p := filepath.Join(r.Out, f)
			info, err := os.Stat(p)
			if err != nil {
				continue
			}
			fmt.Fprintf(h, "%s %d %d\n", f, info.Size(), info.ModTime().UnixNano())
			if strings.HasSuffix(f, ".efi") {
				if fh, err := os.Open(p); err == nil {
					_, _ = io.Copy(h, fh)
					fh.Close()
				}
			}
		}
	}
	fmt.Fprintf(h, "build=%v\n", r.Build)
	return hex.EncodeToString(h.Sum(nil))
}

// addID notes an install's machine ID; false when another install of the
// run has it.
func (r *Run) addID(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.ids, id) {
		return false
	}
	r.ids = append(r.ids, id)
	return true
}

func (r *Run) idCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ids)
}

// skip notes a part of a step left out on a version from before it; true
// the first time.
func (r *Run) skip(what string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.skipped, what) {
		return false
	}
	r.skipped = append(r.skipped, what)
	return true
}

// roomUsed notes the most the machines' files have taken so far.
func (r *Run) roomUsed() {
	n := vm.Used(r.VM)
	r.mu.Lock()
	if n > r.peak {
		r.peak = n
	}
	r.mu.Unlock()
}

// The scenarios' order of running, their dependencies and snapshots.

type outcome int

const (
	pending outcome = iota
	passed
	failed
	leftOut
	resumed
)

func (r *Run) runScenarios(t *testing.T) {
	want := map[string]bool{}
	var add func(n string)
	add = func(n string) {
		if want[n] {
			return
		}
		want[n] = true
		s := scenarioByName(n)
		if s.from != "" && !(r.Resume && r.snapshotValid(s.from)) {
			add(producer(s.from).name)
		}
	}
	if len(r.Only) == 0 {
		for _, s := range scenarios {
			add(s.name)
		}
	} else {
		for _, n := range r.Only {
			add(n)
		}
	}

	results := map[string]outcome{}
	done := map[string]chan struct{}{}
	for _, s := range scenarios {
		done[s.name] = make(chan struct{})
	}
	var mu sync.Mutex
	get := func(n string) outcome {
		mu.Lock()
		defer mu.Unlock()
		return results[n]
	}
	consumers := map[string][]string{}
	for _, s := range scenarios {
		if want[s.name] && s.from != "" {
			consumers[s.from] = append(consumers[s.from], s.name)
		}
	}
	// runOne runs a scenario once its producer has passed; one whose
	// producer did not is skipped.
	runOne := func(s *scenario) {
		o := func() outcome {
			if s.from != "" {
				if p := producer(s.from); want[p.name] {
					<-done[p.name]
					if get(p.name) != passed {
						r.P.Plain(fmt.Sprintf("image-check: scenario %s is skipped: %s did not pass", s.name, p.name))
						return failed
					}
				}
			}
			if s.leftOut != nil && s.leftOut(r) != "" {
				return leftOut
			}
			if t.Run(s.name, func(t *testing.T) { r.runScenario(t, s) }) {
				return passed
			}
			return failed
		}()
		mu.Lock()
		results[s.name] = o
		mu.Unlock()
		close(done[s.name])
		r.gc(want, consumers, get)
	}
	if r.Jobs == 1 {
		// In the table's order, as the steps are numbered.
		for _, s := range scenarios {
			if want[s.name] {
				runOne(s)
			} else {
				close(done[s.name])
			}
		}
	} else {
		sem := make(chan struct{}, r.Jobs)
		var wg sync.WaitGroup
		for _, s := range scenarios {
			if !want[s.name] {
				close(done[s.name])
				continue
			}
			wg.Add(1)
			go func(s *scenario) {
				defer wg.Done()
				if s.from != "" {
					if p := producer(s.from); want[p.name] {
						<-done[p.name]
					}
				}
				sem <- struct{}{}
				defer func() { <-sem }()
				runOne(s)
			}(s)
		}
		wg.Wait()
	}
	r.roomUsed()

	var bad []string
	for _, s := range scenarios {
		if want[s.name] && results[s.name] == failed {
			bad = append(bad, s.name)
		}
	}
	if len(bad) > 0 {
		r.P.Plain(fmt.Sprintf("\nimage-check failed in %s: %s (logs in %s)", report.Took(r.P.Since()), strings.Join(bad, ", "), r.VM))
		t.Fail()
		return
	}
	line := fmt.Sprintf("\nimage-check passed in %s: %s%s%s (the disks of the VM took at most %d MB)",
		report.Took(r.P.Since()), r.Version, ifs(r.Next != "", " → "+r.Next), ifs(r.Broken != "", ", fell back from "+r.Broken), r.peak/1_000_000)
	if len(r.Only) > 0 {
		line = strings.Replace(line, "passed", "passed ("+strings.Join(r.Only, ", ")+")", 1)
	}
	r.P.Plain(line)
	if len(r.skipped) > 0 {
		r.P.Plain("not checked, on a version from before it: " + strings.Join(r.skipped, ", "))
	}
}

// gc deletes the snapshots every scenario that needs them has passed with,
// and whose own children are gone. RESUME and KEEP keep them all.
func (r *Run) gc(want map[string]bool, consumers map[string][]string, get func(string) outcome) {
	if r.Resume || r.Keep {
		return
	}
	for changed := true; changed; {
		changed = false
		for _, s := range scenarios {
			if s.saves == "" || !exists(r.snapDir(s.saves)) {
				continue
			}
			can := true
			for _, c := range consumers[s.saves] {
				if get(c) != passed && get(c) != leftOut {
					can = false
				}
			}
			for _, child := range scenarios {
				if child.from == s.saves && child.saves != "" && exists(r.snapDir(child.saves)) {
					can = false
				}
			}
			if can {
				r.roomUsed()
				os.RemoveAll(r.snapDir(s.saves))
				changed = true
			}
		}
	}
}

func (r *Run) snapDir(name string) string { return filepath.Join(r.VM, "snap", name) }

// snapMeta is what a snapshot records besides its disks.
type snapMeta struct {
	Key     string   `json:"key"`
	Harness string   `json:"harness"`
	State   State    `json:"state"`
	IDs     []string `json:"ids"`
	Skipped []string `json:"skipped"` // the parts left out so far
	Disks   []string `json:"disks"`
}

// snapKey is the key of a snapshot: the images and the scenarios that made
// it.
func (r *Run) snapKey(name string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", r.imageKey(), strings.Join(r.Versions, " "))
	for p := producer(name); p != nil; {
		fmt.Fprintf(h, "%s\n", p.name)
		if p.from == "" {
			break
		}
		p = producer(p.from)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (r *Run) readSnap(name string) (snapMeta, error) {
	var m snapMeta
	b, err := os.ReadFile(filepath.Join(r.snapDir(name), "meta.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

// snapshotValid says whether RESUME may use a snapshot: it is there and was
// made from the same images. A snapshot made by other harness code is used
// with a warning: a fix to the check is what RESUME is for.
func (r *Run) snapshotValid(name string) bool {
	m, err := r.readSnap(name)
	if err != nil {
		return false
	}
	if m.Key != r.snapKey(name) {
		r.P.Plain(fmt.Sprintf("image-check: snapshot %s is of other images: made again", name))
		os.RemoveAll(r.snapDir(name))
		return false
	}
	if m.Harness != r.Harness {
		r.P.Plain(fmt.Sprintf("image-check: snapshot %s was made by other check code (%.12s, now %.12s): resumed all the same", name, m.Harness, r.Harness))
	}
	r.P.Plain(fmt.Sprintf("image-check: resuming from snapshot %s", name))
	return true
}
