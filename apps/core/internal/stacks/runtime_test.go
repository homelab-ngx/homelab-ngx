package stacks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/notifications"
)

// fakeHost is a machine made of the commands the runtime runs: nix builds a
// system per commit, nix-env keeps the profile's generations, systemd-run and
// systemctl start and stop the one container, and a system named in
// unhealthy fails its health check while it is the profile's current one.
type fakeHost struct {
	*cmdrunnertest.Fake
	mu        sync.Mutex
	running   bool
	gens      []string // system path per generation, index 0 is generation 1
	current   int      // generation the profile points at
	unhealthy map[string]bool
	caddyDown bool
	noSwitch  bool // switch-to-configuration fails
	noStop    bool // systemctl stop fails and the container keeps running
	sameBuild bool // every commit builds the same system
	orphan    bool // a container is registered although its unit is gone
	sent      []notifications.Message
	problems  map[string]string // open problems by key
}

func (h *fakeHost) Send(_ context.Context, m notifications.Message) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, m)
	return "", nil
}

func (h *fakeHost) Report(_ context.Context, p notifications.Problem) (notifications.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.problems[p.Key] = p.Title + ": " + p.Detail + " [" + p.Path + "]"
	return notifications.Result{}, nil
}

func (h *fakeHost) Resolve(_ context.Context, key string) (notifications.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.problems, key)
	return notifications.Result{}, nil
}

func (h *fakeHost) Reconcile(_ context.Context, scope string, current []notifications.Problem) (notifications.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k := range h.problems {
		if strings.HasPrefix(k, scope) {
			delete(h.problems, k)
		}
	}
	for _, p := range current {
		h.problems[p.Key] = p.Title + ": " + p.Detail
	}
	return notifications.Result{}, nil
}

func (h *fakeHost) currentSystem() string {
	if h.current == 0 {
		return ""
	}
	return h.gens[h.current-1]
}

func ok(stdout string) (cmdrunner.Result, error) {
	return cmdrunner.Result{Stdout: stdout, Output: stdout}, nil
}

func fail(code int, out string) (cmdrunner.Result, error) {
	res := cmdrunner.Result{ExitCode: code, Stdout: out, Output: out}
	return res, &cmdrunner.Error{Result: res, Err: errors.New("exit status")}
}

// stackURLRE finds the stack's reference in a wrapper flake.
var stackURLRE = regexp.MustCompile(`inputs\.stack\.url = "([^"]+)";`)

// builtRev is the commit a nix build builds: the last path element of the
// stack's reference, read from the wrapper flake when it builds one.
func builtRev(c cmdrunner.Cmd) string {
	ref, _, _ := strings.Cut(c.Args[len(c.Args)-1], "#")
	if dir, ok := strings.CutPrefix(ref, "path:"); ok {
		if flake, err := os.ReadFile(filepath.Join(dir, "flake.nix")); err == nil {
			if m := stackURLRE.FindSubmatch(flake); m != nil {
				ref = string(m[1])
			}
		}
	}
	return ref[strings.LastIndex(ref, "/")+1:]
}

func newFakeHost() *fakeHost {
	h := &fakeHost{Fake: cmdrunnertest.NewFake(), unhealthy: map[string]bool{}, problems: map[string]string{}}
	locked := func(fn func(c cmdrunner.Cmd) (cmdrunner.Result, error)) func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		return func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return fn(c)
		}
	}
	h.On("nix").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		// A wrapper flake whose stack is "github:o/r/<rev>" builds
		// /nix/store/<rev>-system.
		rev := builtRev(c)
		if h.sameBuild {
			rev = "same"
		}
		return ok("/nix/store/" + rev + "-system\n")
	})
	h.On("nix-env").Do(locked(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		switch c.Args[2] {
		case "--set":
			// Like nix: the newest generation is reused when it is the same system.
			if n := len(h.gens); n == 0 || h.gens[n-1] != c.Args[3] {
				h.gens = append(h.gens, c.Args[3])
			}
			h.current = len(h.gens)
		case "--switch-generation":
			fmt.Sscan(c.Args[3], &h.current)
		case "--list-generations":
			var b strings.Builder
			for i := range h.gens {
				fmt.Fprintf(&b, "  %d   2026-11-15 22:00:00   ", i+1)
				if i+1 == h.current {
					b.WriteString("(current)")
				}
				b.WriteByte('\n')
			}
			return ok(b.String())
		}
		return ok("")
	}))
	h.On("systemd-run").Do(locked(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		if strings.HasPrefix(c.Args[0], "--machine=") { // switch-to-configuration
			if !h.running || h.noSwitch {
				return fail(2, "Failed to run activate script")
			}
			return ok("")
		}
		h.running = true
		return ok("")
	}))
	h.On("systemctl", "stop").Do(locked(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		switch {
		case h.noStop:
			return fail(1, "Job for homelab-stack-immich.service canceled.")
		case !h.running:
			return fail(5, "Unit homelab-stack-immich.service not loaded.")
		}
		h.running = false
		return ok("")
	}))
	h.On("systemctl", "reset-failed").Reply("")
	h.On("systemctl", "is-active").Do(locked(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		if h.running {
			return ok("active\n")
		}
		return fail(3, "inactive\n")
	}))
	h.On("systemctl", "--machine=immich", "is-active").Do(locked(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		if !h.running || h.unhealthy[h.currentSystem()] {
			return fail(3, "failed\n")
		}
		return ok("active\n")
	}))
	h.On("systemctl", "reload", "caddy.service").Do(locked(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		if h.caddyDown {
			return fail(1, "adapting config: bad")
		}
		return ok("")
	}))
	h.On("machinectl", "show").Do(locked(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		if h.orphan {
			return ok("State=running\n")
		}
		return fail(1, "Could not get path to machine: No machine known")
	}))
	h.On("machinectl", "terminate").Do(locked(func(cmdrunner.Cmd) (cmdrunner.Result, error) { h.orphan = false; return ok("") }))
	h.On("nft").Reply("")
	h.On("ip").Reply("")
	return h
}

// newRuntime returns a service on a fake host with its directories under
// the test's temp dir, and immich installed and approved at commit aaaaaaa1.
func newRuntime(t *testing.T) (*Service, *fakeSource, *fakeHost) {
	t.Helper()
	svc, fs, h := newServiceOn(t)
	fs.set("aaaaaaa1", immichManifest)
	if _, err := svc.Add(context.Background(), src, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(context.Background(), "immich", "aaaaaaa1"); err != nil {
		t.Fatal(err)
	}
	return svc, fs, h
}

// release fetches and approves a new commit of immich.
func release(t *testing.T, svc *Service, fs *fakeSource, rev, manifest string) {
	t.Helper()
	ctx := context.Background()
	fs.set(rev, manifest)
	if _, err := svc.Fetch(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	if s, _ := svc.Get(ctx, "immich"); s.Approved.Rev != rev {
		if _, err := svc.Approve(ctx, "immich", rev); err != nil {
			t.Fatal(err)
		}
	}
}

// finished runs one operation on immich to its end and returns the stack
// after it.
func finished(t *testing.T, svc *Service, launch func(context.Context, string) (Stack, error)) Stack {
	t.Helper()
	return finishedOn(t, svc, "immich", launch)
}

// finishedOn is finished for the stack name.
func finishedOn(t *testing.T, svc *Service, name string, launch func(context.Context, string) (Stack, error)) Stack {
	t.Helper()
	ctx := context.Background()
	if _, err := launch(ctx, name); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	o := svc.ops[name]
	svc.mu.Unlock()
	select {
	case <-o.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("the %s did not finish; phase %q", o.kind, o.phase)
	}
	s, err := svc.Get(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func argv(c cmdrunner.Cmd) string { return c.Name + " " + strings.Join(c.Args, " ") }

func TestStartBuildsTheApprovedCommitAndBootsItWithItsGrants(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	s := finished(t, svc, svc.Start)

	if s.Operation.Error != "" || !s.Running || !s.WantRunning || s.Generation != 1 || s.Address != "10.99.0.2" {
		t.Fatalf("got running=%v want=%v generation=%d address=%q operation=%+v", s.Running, s.WantRunning, s.Generation, s.Address, s.Operation)
	}
	if len(s.Generations) != 1 || s.Generations[0].Rev != "aaaaaaa1" || s.Generations[0].Failed {
		t.Errorf("generations = %+v", s.Generations)
	}

	boots := h.CallsMatching("systemd-run", "--unit=homelab-stack-immich")
	if len(boots) != 1 {
		t.Fatalf("container boots = %d, want 1", len(boots))
	}
	boot := argv(boots[0])
	for _, want := range []string{
		"--private-users=pick", "--network-veth", "--bind-ro=/nix/store",
		"--bind=/pool/immich:/pool/immich:idmap", // the storage grant
		"--bind=/dev/dri",                        // the GPU grant
		"--bind-ro=" + svc.profileDir("immich") + ":/nix/var/nix/profiles",
		"-D " + filepath.Join(svc.stateDir("immich"), "root"),
	} {
		if !strings.Contains(boot, want) {
			t.Errorf("boot command lacks %q:\n%s", want, boot)
		}
	}

	// The firewall is loaded before the container exists.
	calls := h.Calls()
	nft := slices.IndexFunc(calls, func(c cmdrunner.Cmd) bool { return c.Name == "nft" })
	run := slices.IndexFunc(calls, func(c cmdrunner.Cmd) bool { return c.Name == "systemd-run" })
	if nft < 0 || nft > run {
		t.Errorf("nft ran at %d, the container started at %d", nft, run)
	}
	if got := argv(h.CallsMatching("ip", "address")[0]); got != "ip address replace 10.99.0.1/24 dev ve-immich" {
		t.Errorf("host address: %s", got)
	}

	route, err := os.ReadFile(svc.routeFile("immich"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "@stack_immich_0 host immich.example.com\nhandle @stack_immich_0 {\n\trequest_body {\n\t\tmax_size 50GB\n\t}\n\treverse_proxy 10.99.0.2:2283\n}\n"; string(route) != want {
		t.Errorf("route file:\n%s", route)
	}
	net, _ := os.ReadFile(svc.netFile("immich"))
	if !strings.Contains(string(net), "Address=10.99.0.2/24\nGateway=10.99.0.1\n") {
		t.Errorf("network file:\n%s", net)
	}
}

// A stack whose units wait for the network reports ready only once its link
// works, so the host end gets its address while the boot is under way.
func TestStartConfiguresTheLinkBeforeTheStackIsReady(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	// nspawn has not created the interface at the first look.
	h.On("ip", "address").FailCode(1, `Cannot find device "ve-immich"`).Reply("")
	h.On("systemd-run", "--unit=homelab-stack-immich").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		deadline := time.Now().Add(5 * time.Second)
		for len(h.CallsMatching("ip", "link", "set", "ve-immich", "up")) == 0 {
			if time.Now().After(deadline) {
				return fail(1, "Job for homelab-stack-immich.service failed: the network never came up")
			}
			time.Sleep(10 * time.Millisecond)
		}
		h.mu.Lock()
		h.running = true
		h.mu.Unlock()
		return ok("")
	})

	s := finished(t, svc, svc.Start)
	if s.Operation.Error != "" || !s.Running {
		t.Fatalf("running=%v operation=%+v", s.Running, s.Operation)
	}
}

func TestStartRefusals(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newServiceOn(t)
	ctx := context.Background()
	_, err := svc.Start(ctx, "immich")
	wantCode(t, err, 404, "unknown_stack")

	fs.set("aaaaaaa1", immichManifest)
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Start(ctx, "immich")
	wantCode(t, err, 409, "not_approved")
	_, err = svc.Update(ctx, "immich")
	wantCode(t, err, 409, "not_approved")
	_, err = svc.Rollback(ctx, "immich", 0)
	wantCode(t, err, 409, "no_previous_generation")
}

func TestOneOperationPerStackAtATime(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	ctx := context.Background()
	release := make(chan struct{})
	h.On("nix", "--extra-experimental-features", "nix-command flakes", "build").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		<-release
		return ok("/nix/store/aaaaaaa1-system\n")
	})
	s, err := svc.Start(ctx, "immich")
	if err != nil {
		t.Fatal(err)
	}
	if s.Operation == nil || s.Operation.Kind != "start" || s.Operation.FinishedAt != nil {
		t.Errorf("operation = %+v", s.Operation)
	}
	_, err = svc.Stop(ctx, "immich")
	wantCode(t, err, 409, "busy")
	wantCode(t, svc.Remove(ctx, "immich"), 409, "busy")
	close(release)
	<-svc.ops["immich"].done
}

func TestUpdateSwitchesARunningStackInPlace(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	finished(t, svc, svc.Start)
	_, err := svc.Update(context.Background(), "immich")
	wantCode(t, err, 409, "up_to_date")

	release(t, svc, fs, "bbbbbbb2", strings.Replace(immichManifest, `"port": 2283, "max_body": "50GB"`, `"port": 3001`, 1))
	s := finished(t, svc, svc.Update)

	if s.Operation.Error != "" || s.Generation != 2 || !s.Running {
		t.Fatalf("generation=%d running=%v operation=%+v", s.Generation, s.Running, s.Operation)
	}
	if n := len(h.CallsMatching("systemd-run", "--machine=immich")); n != 1 {
		t.Errorf("switches inside the stack = %d, want 1", n)
	}
	if n := len(h.CallsMatching("systemd-run", "--unit=homelab-stack-immich")); n != 1 {
		t.Errorf("the container was booted %d times; an update with the same binds must not restart it", n)
	}
	// The new manifest's route replaced the old one.
	if route, _ := os.ReadFile(svc.routeFile("immich")); !strings.Contains(string(route), "reverse_proxy 10.99.0.2:3001") || strings.Contains(string(route), "max_size") {
		t.Errorf("route file:\n%s", route)
	}
}

func TestUpdateRollsBackAnUnhealthyGeneration(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	svc.healthTimeout = 0
	finished(t, svc, svc.Start)

	h.unhealthy["/nix/store/bbbbbbb2-system"] = true
	release(t, svc, fs, "bbbbbbb2", strings.Replace(immichManifest, `"port": 2283, "max_body": "50GB"`, `"port": 3001`, 1))
	s := finished(t, svc, svc.Update)

	if s.Generation != 1 || !s.Running {
		t.Fatalf("generation=%d running=%v, want the stack back on generation 1", s.Generation, s.Running)
	}
	if h.current != 1 {
		t.Errorf("the profile points at generation %d", h.current)
	}
	if e := s.Operation.Error; !strings.Contains(e, "immich-server.service is failed") || !strings.Contains(e, "rolled back to generation 1") {
		t.Errorf("operation error = %q", e)
	}
	if len(s.Generations) != 2 || !s.Generations[0].Failed || s.Generations[1].Failed {
		t.Errorf("generations = %+v", s.Generations)
	}
	if len(h.sent) != 1 || h.sent[0].Title != "Stack immich: update rolled back" || h.sent[0].Severity != notifications.Alert {
		t.Errorf("notifications = %+v", h.sent)
	}
	// The old generation's route is back.
	if route, _ := os.ReadFile(svc.routeFile("immich")); !strings.Contains(string(route), "reverse_proxy 10.99.0.2:2283") {
		t.Errorf("route file after the rollback:\n%s", route)
	}

	// The failed generation is not a rollback target, and the same commit
	// can be tried again once it is fixed.
	_, err := svc.Rollback(context.Background(), "immich", 0)
	wantCode(t, err, 409, "no_previous_generation")
	h.unhealthy = map[string]bool{}
	if s = finished(t, svc, svc.Update); s.Generation != 2 || s.Operation.Error != "" || s.Generations[0].Failed {
		t.Errorf("retry: generation=%d operation=%+v generations=%+v", s.Generation, s.Operation, s.Generations)
	}
}

func TestUpdateRestartsTheContainerWhenTheBindsChange(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	finished(t, svc, svc.Start)
	release(t, svc, fs, "bbbbbbb2", strings.Replace(immichManifest, `"variables": [`,
		`"variables": [{"name": "EXTRA", "kind": "path", "required": true, "default": "/pool/photos", "mount": "/photos", "read_only": true},`, 1))
	s := finished(t, svc, svc.Update)

	if s.Operation.Error != "" || s.Generation != 2 || !s.Running {
		t.Fatalf("generation=%d running=%v operation=%+v", s.Generation, s.Running, s.Operation)
	}
	boots := h.CallsMatching("systemd-run", "--unit=homelab-stack-immich")
	if len(boots) != 2 || !strings.Contains(argv(boots[1]), "--bind-ro=/pool/photos:/photos:idmap") {
		t.Errorf("boots = %d; a new bind needs a new container", len(boots))
	}
	if n := len(h.CallsMatching("systemd-run", "--machine=immich")); n != 0 {
		t.Errorf("switched in place %d times", n)
	}
}

func TestUpdateRestartsTheContainerWhenTheSwitchFails(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	finished(t, svc, svc.Start)
	h.noSwitch = true
	release(t, svc, fs, "bbbbbbb2", immichManifest)
	s := finished(t, svc, svc.Update)

	if s.Operation.Error != "" || s.Generation != 2 || !s.Running {
		t.Fatalf("generation=%d running=%v operation=%+v", s.Generation, s.Running, s.Operation)
	}
	if n := len(h.CallsMatching("systemd-run", "--unit=homelab-stack-immich")); n != 2 {
		t.Errorf("container boots = %d, want 2", n)
	}
}

func TestUpdateOfAStoppedStackOnlyBuilds(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	s := finished(t, svc, svc.Update)
	if s.Generation != 1 || s.Running || s.WantRunning || s.Operation.Error != "" {
		t.Errorf("generation=%d running=%v operation=%+v", s.Generation, s.Running, s.Operation)
	}
	if n := len(h.CallsMatching("systemd-run")); n != 0 {
		t.Errorf("systemd-run ran %d times", n)
	}
}

func TestRollbackGoesToTheGenerationBefore(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	finished(t, svc, svc.Start)
	release(t, svc, fs, "bbbbbbb2", immichManifest)
	finished(t, svc, svc.Update)

	s := finished(t, svc, func(ctx context.Context, name string) (Stack, error) { return svc.Rollback(ctx, name, 0) })
	if s.Generation != 1 || h.current != 1 || s.Operation.Error != "" || s.Generations[0].Failed {
		t.Errorf("generation=%d profile=%d operation=%+v", s.Generation, h.current, s.Operation)
	}
	if n := len(h.CallsMatching("systemd-run", "--machine=immich")); n != 2 {
		t.Errorf("switches inside the stack = %d, want 2 (update, rollback)", n)
	}
}

func TestStopTakesRoutesAndRulesAway(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	finished(t, svc, svc.Start)
	s := finished(t, svc, svc.Stop)

	if s.Running || s.WantRunning || s.Operation.Error != "" {
		t.Errorf("running=%v want=%v operation=%+v", s.Running, s.WantRunning, s.Operation)
	}
	if exists(svc.routeFile("immich")) {
		t.Error("the route file is still there")
	}
	if n := len(h.CallsMatching("nft", "destroy", "table", "inet", "homelab_stack_immich")); n != 1 {
		t.Errorf("rule removals = %d", n)
	}
	if n := len(h.CallsMatching("systemctl", "reload", "caddy.service")); n != 2 {
		t.Errorf("caddy reloads = %d, want 2 (start, stop)", n)
	}
}

func TestRoutesCaddyRefusesAreTakenBack(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	h.caddyDown = true
	s := finished(t, svc, svc.Start)
	if !strings.Contains(s.Operation.Error, "caddy refused the routes") {
		t.Errorf("operation = %+v", s.Operation)
	}
	if exists(svc.routeFile("immich")) {
		t.Error("a route file Caddy refused was left in place")
	}
}

func TestRemoveNeedsAStoppedStackAndKeepsItsState(t *testing.T) {
	t.Parallel()
	svc, _, _ := newRuntime(t)
	ctx := context.Background()
	finished(t, svc, svc.Start)
	wantCode(t, svc.Remove(ctx, "immich"), 409, "stack_running")

	finished(t, svc, svc.Stop)
	if err := svc.Remove(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	if exists(svc.profileDir("immich")) {
		t.Error("the profile, and with it the GC root, is still there")
	}
	if !exists(svc.netFile("immich")) {
		t.Error("the state directory was removed with the stack")
	}
}

func TestStartWantedStartsOnlyWhatTheOperatorLeftRunning(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	svc.StartWanted(context.Background())
	if h.running {
		t.Fatal("a stack nobody started was started at boot")
	}
	finished(t, svc, svc.Start)

	// A reboot: nothing runs, the rules and addresses are gone.
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
	svc.StartWanted(context.Background())
	if !h.running {
		t.Error("the stack was not started again")
	}
	if n := len(h.CallsMatching("nix", "--extra-experimental-features", "nix-command flakes", "build")); n != 1 {
		t.Errorf("builds = %d; a start after a reboot must not build", n)
	}
	if len(h.sent) != 0 {
		t.Errorf("notifications = %+v", h.sent)
	}
}

func TestAllocSubnetTakesTheLowestFree(t *testing.T) {
	t.Parallel()
	if got, _ := allocSubnet(nil); got != "10.99.0.0/24" {
		t.Errorf("first = %s", got)
	}
	if got, _ := allocSubnet([]string{"10.99.0.0/24", "", "10.99.2.0/24"}); got != "10.99.1.0/24" {
		t.Errorf("gap = %s", got)
	}
	all := make([]string, subnetCount)
	for i := range all {
		all[i] = subnetAt(i)
	}
	if _, err := allocSubnet(all); err == nil {
		t.Error("a full pool allocated a subnet")
	}
}

func TestSecondStackGetsItsOwnSubnet(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newRuntime(t)
	ctx := context.Background()
	finished(t, svc, svc.Start)
	fs.set("ccccccc1", `{"version":7,"name":"email","nixpkgs":"nixos-unstable"}`)
	if _, err := svc.Add(ctx, "github:example/stack-email", ""); err != nil {
		t.Fatal(err)
	}
	subnet, err := svc.ensureSubnet(ctx, "email")
	if err != nil || subnet != "10.99.1.0/24" {
		t.Errorf("subnet = %q, %v", subnet, err)
	}
	if again, _ := svc.ensureSubnet(ctx, "email"); again != subnet {
		t.Errorf("the subnet changed to %q", again)
	}
}

func TestNftRulesetDeniesByDefaultAndOpensGrants(t *testing.T) {
	t.Parallel()
	m := mustParse(t, `{"version":1,"name":"my-app","egress":[
		{"to":"host","port":25},
		{"to":"192.168.1.10/32","port":53,"proto":"udp"},
		{"to":"fd00::/64","port":443}]}`)
	got, err := nftRuleset("my-app", "10.99.3.0/24", m.Egress, m.Ports, false)
	if err != nil {
		t.Fatal(err)
	}
	want := `table inet homelab_stack_my_app
delete table inet homelab_stack_my_app
table inet homelab_stack_my_app {
	chain input {
		type filter hook input priority filter - 10; policy accept;
		iifname != "ve-my-app" return
		ct state established,related accept
		tcp dport 443 accept
		tcp dport 25 accept
		ip daddr 192.168.1.10/32 udp dport 53 accept
		ip6 daddr fd00::/64 tcp dport 443 accept
		reject with icmpx admin-prohibited
	}
	chain forward {
		type filter hook forward priority filter - 10; policy accept;
		oifname "ve-my-app" ct state established,related accept
		oifname "ve-my-app" drop
		iifname != "ve-my-app" return
		ct state established,related accept
		ip daddr 192.168.1.10/32 udp dport 53 accept
		ip6 daddr fd00::/64 tcp dport 443 accept
		ip daddr { 10.0.0.0/8, 100.64.0.0/10, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16 } reject with icmpx admin-prohibited
		meta nfproto ipv6 reject with icmpx admin-prohibited
		accept
	}
	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr 10.99.3.0/24 oifname != "ve-my-app" masquerade
	}
}
`
	if got != want {
		t.Errorf("ruleset:\n%s", got)
	}
	if strings.Contains(got, "7781") {
		t.Errorf("the stack listener is open without an api grant")
	}
	withAPI, err := nftRuleset("my-app", "10.99.3.0/24", m.Egress, m.Ports, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withAPI, "\t\ttcp dport 443 accept\n\t\ttcp dport 7781 accept\n") {
		t.Errorf("the api grant did not open the stack listener:\n%s", withAPI)
	}
	if strings.Count(withAPI, "7781") != 1 {
		t.Errorf("the stack listener is open beyond the input chain:\n%s", withAPI)
	}
}

func TestNftRulesetForwardsGrantedPortsFromNamedClientsOnly(t *testing.T) {
	t.Parallel()
	m := mustParse(t, `{"version":2,"name":"media","ports":[
		{"port":32400,"from":["192.168.1.0/24"]},
		{"port":8555,"to":8556,"proto":"udp","from":["192.168.1.102/32","192.168.1.236/32"]}]}`)
	got, err := nftRuleset("media", "10.99.1.0/24", m.Egress, m.Ports, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\t\toifname \"ve-media\" ct state established,related accept\n" +
			"\t\toifname \"ve-media\" ip saddr { 192.168.1.0/24 } ip daddr 10.99.1.2 tcp dport 32400 ct status dnat accept\n" +
			"\t\toifname \"ve-media\" ip saddr { 192.168.1.102/32, 192.168.1.236/32 } ip daddr 10.99.1.2 udp dport 8556 ct status dnat accept\n" +
			"\t\toifname \"ve-media\" drop\n",
		"\tchain prerouting {\n" +
			"\t\ttype nat hook prerouting priority dstnat; policy accept;\n" +
			"\t\tip saddr { 192.168.1.0/24 } fib daddr type local tcp dport 32400 dnat ip to 10.99.1.2:32400\n" +
			"\t\tip saddr { 192.168.1.102/32, 192.168.1.236/32 } fib daddr type local udp dport 8555 dnat ip to 10.99.1.2:8556\n" +
			"\t}\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ruleset lacks\n%s\ngot:\n%s", want, got)
		}
	}
	none, _ := nftRuleset("immich", "10.99.0.0/24", nil, nil, false)
	if strings.Contains(none, "prerouting") || strings.Contains(none, "dnat") {
		t.Errorf("a stack without ports got a forward:\n%s", none)
	}
}

func TestStopOfAStoppedStackSucceeds(t *testing.T) {
	t.Parallel()
	svc, _, _ := newRuntime(t)
	if s := finished(t, svc, svc.Stop); s.Operation.Error != "" || s.Operation.Phase != "stopped" {
		t.Errorf("operation = %+v", s.Operation)
	}
}

func TestStopKeepsTheFirewallWhileTheContainerRuns(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	finished(t, svc, svc.Start)
	h.noStop = true
	s := finished(t, svc, svc.Stop)

	if !strings.Contains(s.Operation.Error, "still running") || !s.Running {
		t.Errorf("running=%v operation=%+v", s.Running, s.Operation)
	}
	if n := len(h.CallsMatching("nft", "destroy")); n != 0 {
		t.Errorf("the firewall of a running container was removed %d times", n)
	}
	wantCode(t, svc.Remove(context.Background(), "immich"), 409, "stack_running")
}

func TestUpdateThatFailsOnTheSameSystemKeepsTheOldCommit(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	h.sameBuild = true
	finished(t, svc, svc.Start)

	// Only the manifest changes, and Caddy refuses its route.
	release(t, svc, fs, "bbbbbbb2", strings.Replace(immichManifest, `"port": 2283, "max_body": "50GB"`, `"port": 3001`, 1))
	h.caddyDown = true
	s := finished(t, svc, svc.Update)

	if s.Generation != 1 || len(s.Generations) != 1 || s.Generations[0].Rev != "aaaaaaa1" || s.Generations[0].Failed {
		t.Fatalf("generation=%d generations=%+v", s.Generation, s.Generations)
	}
	if !strings.Contains(s.Operation.Error, "generation 1 is back on commit aaaaaaa1") {
		t.Errorf("operation error = %q", s.Operation.Error)
	}
	if route, _ := os.ReadFile(svc.routeFile("immich")); !strings.Contains(string(route), "reverse_proxy 10.99.0.2:2283") {
		t.Errorf("route file after the failed update:\n%s", route)
	}
	// The commit can be tried again.
	h.caddyDown = false
	if s = finished(t, svc, svc.Update); s.Operation.Error != "" || s.Generations[0].Rev != "bbbbbbb2" {
		t.Errorf("retry: operation=%+v generations=%+v", s.Operation, s.Generations)
	}
}

func TestStartLeavesAnExistingContainerRootAlone(t *testing.T) {
	t.Parallel()
	svc, _, _ := newRuntime(t)
	finished(t, svc, svc.Start)
	finished(t, svc, svc.Stop)

	// The stack replaced a directory in its root with a link to a host path.
	target := filepath.Join(t.TempDir(), "host-dir")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	usr := filepath.Join(svc.stateDir("immich"), "root", "usr")
	if err := os.Remove(usr); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, usr); err != nil {
		t.Fatal(err)
	}
	finished(t, svc, svc.Start)
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o700 {
		t.Errorf("a start changed the mode of a directory outside the stack to %o", fi.Mode().Perm())
	}
}

func TestHTTPProbeNeeds2xxAndFollowsNoRedirect(t *testing.T) {
	t.Parallel()
	var elsewhere atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
		case "/moved":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case "/elsewhere":
			elsewhere.Add(1)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if err := httpProbe(ctx, srv.URL+"/ok"); err != nil {
		t.Errorf("200: %v", err)
	}
	if err := httpProbe(ctx, srv.URL+"/broken"); err == nil {
		t.Error("a 500 passed the probe")
	}
	if err := httpProbe(ctx, srv.URL+"/moved"); err == nil || elsewhere.Load() != 0 {
		t.Errorf("a redirect passed the probe or was followed: err=%v, followed %d times", err, elsewhere.Load())
	}
}

func TestHealthCheckProbesTheStackAddress(t *testing.T) {
	t.Parallel()
	svc, _, _ := newRuntime(t)
	var probed string
	svc.probe = func(_ context.Context, url string) error { probed = url; return errors.New("connection refused") }
	svc.healthTimeout = 0
	s := finished(t, svc, svc.Start)
	if probed != "http://10.99.0.2:2283/api/server/ping" {
		t.Errorf("probed %q", probed)
	}
	if !strings.Contains(s.Operation.Error, "started but not healthy") || !strings.Contains(s.Operation.Error, "connection refused") {
		t.Errorf("operation error = %q", s.Operation.Error)
	}
}

// restarted is a new daemon on the same database and host.
func restarted(svc *Service, h *fakeHost) *Service {
	next := New(svc.st, svc.src, svc.host, h, nil)
	next.now, next.probe, next.healthEvery, next.healthTimeout = svc.now, svc.probe, time.Millisecond, 0
	return next
}

// interruptedUpdate starts immich, then updates it to commit bbbbbbb2 and
// shuts the daemon down once the update reaches the phase containing at.
// afterShutdown runs once the daemon is told to stop, before the update is
// waited for: to let a step blocked in a fake command finish only then.
func interruptedUpdate(t *testing.T, svc *Service, fs *fakeSource, at string, afterShutdown ...func()) {
	t.Helper()
	ctx, shutdown := context.WithCancel(context.Background())
	svc.StartWanted(ctx) // nothing to start yet; makes ctx the daemon's lifetime
	finished(t, svc, svc.Start)
	release(t, svc, fs, "bbbbbbb2", immichManifest)
	svc.healthTimeout = time.Hour
	if _, err := svc.Update(context.Background(), "immich"); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	o := svc.ops["immich"]
	svc.mu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for {
		svc.mu.Lock()
		phase := o.phase
		svc.mu.Unlock()
		if strings.Contains(phase, at) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the update never reached %q; phase %q", at, phase)
		}
		time.Sleep(time.Millisecond)
	}
	shutdown()
	for _, f := range afterShutdown {
		f()
	}
	<-o.done
}

func storedOp(t *testing.T, svc *Service) string {
	t.Helper()
	r, err := svc.row(context.Background(), "immich")
	if err != nil {
		t.Fatal(err)
	}
	return r.OpKind + "/" + r.OpStep
}

func TestDaemonRestartRollsBackAnUpdateThatWasNeverHealthy(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	h.unhealthy["/nix/store/bbbbbbb2-system"] = true
	interruptedUpdate(t, svc, fs, "waiting for generation 2")
	if got := storedOp(t, svc); got != "update/activating" {
		t.Fatalf("stored operation = %q", got)
	}

	next := restarted(svc, h)
	next.StartWanted(context.Background())
	s, err := next.Get(context.Background(), "immich")
	if err != nil {
		t.Fatal(err)
	}
	if s.Generation != 1 || h.current != 1 || !s.Running || !s.Generations[0].Failed {
		t.Errorf("generation=%d profile=%d running=%v generations=%+v", s.Generation, h.current, s.Running, s.Generations)
	}
	if s.Operation == nil || s.Operation.Kind != "update" || !strings.Contains(s.Operation.Error, "rolled back to generation 1") {
		t.Errorf("operation = %+v", s.Operation)
	}
	if len(h.sent) != 1 || h.sent[0].Title != "Stack immich: update rolled back" {
		t.Errorf("notifications = %+v", h.sent)
	}
	if got := storedOp(t, next); got != "/" {
		t.Errorf("stored operation after the restart = %q", got)
	}
}

func TestDaemonRestartKeepsAnUpdateThatIsHealthy(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	h.unhealthy["/nix/store/bbbbbbb2-system"] = true
	interruptedUpdate(t, svc, fs, "waiting for generation 2")
	h.mu.Lock()
	h.unhealthy = map[string]bool{} // it came up while the daemon was away
	h.mu.Unlock()

	next := restarted(svc, h)
	next.StartWanted(context.Background())
	s, _ := next.Get(context.Background(), "immich")
	if s.Generation != 2 || s.Generations[0].Failed || s.Operation.Error != "" || len(h.sent) != 0 {
		t.Errorf("generation=%d generations=%+v operation=%+v notifications=%+v", s.Generation, s.Generations, s.Operation, h.sent)
	}
	if got := storedOp(t, next); got != "/" {
		t.Errorf("stored operation after the restart = %q", got)
	}
}

func TestDaemonRestartAbandonsAnUpdateThatWasStillBuilding(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	building := make(chan struct{})
	// The update's build (commit bbbbbbb2) blocks; the first start does not.
	h.On("nix", "--extra-experimental-features", "nix-command flakes", "build").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		rev := builtRev(c)
		if rev == "bbbbbbb2" {
			<-building
		}
		return ok("/nix/store/" + rev + "-system\n")
	})
	// The build finishes only after the daemon was told to stop.
	interruptedUpdate(t, svc, fs, "building", func() { close(building) })
	if got := storedOp(t, svc); got != "update/building" {
		t.Fatalf("stored operation = %q", got)
	}

	next := restarted(svc, h)
	next.StartWanted(context.Background())
	s, _ := next.Get(context.Background(), "immich")
	if s.Generation != 1 || len(s.Generations) != 1 || !s.Running || len(h.sent) != 0 {
		t.Errorf("generation=%d generations=%+v running=%v notifications=%+v", s.Generation, s.Generations, s.Running, h.sent)
	}
	if got := storedOp(t, next); got != "/" {
		t.Errorf("stored operation after the restart = %q", got)
	}
}

func TestMonitorReportsAStackThatStaysUnhealthyAndResolvesIt(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	finished(t, svc, svc.Start)
	ctx, fails := context.Background(), map[string]int{}

	h.unhealthy["/nix/store/aaaaaaa1-system"] = true
	svc.monitorOnce(ctx, fails)
	svc.monitorOnce(ctx, fails)
	if len(h.problems) != 0 {
		t.Fatalf("two failed checks already opened a problem: %v", h.problems)
	}
	svc.monitorOnce(ctx, fails)
	if got := h.problems["stack:immich"]; !strings.Contains(got, "Stack immich is unhealthy: immich-server.service is failed") {
		t.Fatalf("problems = %v", h.problems)
	}
	// The notification opens the stack's page.
	if got := h.problems["stack:immich"]; !strings.HasSuffix(got, "[/stacks/immich]") {
		t.Errorf("problem links elsewhere: %q", got)
	}
	if s, _ := svc.Get(ctx, "immich"); s.Operation == nil || s.Operation.Kind != "start" || !s.Running {
		t.Errorf("a health check replaced the stack's last operation or stopped it: %+v", s.Operation)
	}

	h.unhealthy = map[string]bool{}
	svc.monitorOnce(ctx, fails)
	if len(h.problems) != 0 {
		t.Errorf("a healthy stack still has a problem: %v", h.problems)
	}
}

func TestMonitorStartsAWantedStackThatDied(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	ctx, fails := context.Background(), map[string]int{}
	svc.monitorOnce(ctx, fails)
	if h.running {
		t.Fatal("the monitor started a stack nobody asked to run")
	}
	finished(t, svc, svc.Start)
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
	svc.monitorOnce(ctx, fails)
	if !h.running || len(h.problems) != 0 {
		t.Errorf("running=%v problems=%v", h.running, h.problems)
	}

	finished(t, svc, svc.Stop)
	svc.monitorOnce(ctx, fails)
	if h.running {
		t.Error("the monitor started a stack the operator stopped")
	}
}

// A pass works through the stacks one at a time, so a stop that finishes
// while it checks an earlier stack must hold when it reaches this one.
func TestMonitorKeepsAStopMadeDuringItsPass(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	ctx, fails := context.Background(), map[string]int{}
	// alpha comes before immich in the pass and shares the fake container.
	if _, err := svc.Add(ctx, src, "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(ctx, "alpha", "aaaaaaa1"); err != nil {
		t.Fatal(err)
	}
	// The monitor's check of alpha is when the operator stops immich.
	var armed atomic.Bool
	var once sync.Once
	h.On("systemctl", "--machine=alpha", "is-active").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		if armed.Load() {
			once.Do(func() { finished(t, svc, svc.Stop) })
		}
		return ok("active\n")
	})
	finished(t, svc, svc.Start)
	finishedOn(t, svc, "alpha", svc.Start)

	armed.Store(true)
	svc.monitorOnce(ctx, fails)

	if n := len(h.CallsMatching("systemd-run", "--unit=homelab-stack-immich")); n != 1 {
		t.Errorf("immich booted %d times, want once: the monitor started a stack the operator stopped", n)
	}
	if s, _ := svc.Get(ctx, "immich"); s.WantRunning || s.Operation == nil || s.Operation.Kind != "stop" {
		t.Errorf("want running=%v operation=%+v", s.WantRunning, s.Operation)
	}
}

func TestStartEndsAContainerThatOutlivedItsUnit(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	h.orphan = true
	wantCode(t, svc.Remove(context.Background(), "immich"), 409, "stack_running")

	s := finished(t, svc, svc.Start)
	if s.Operation.Error != "" || !s.Running {
		t.Fatalf("running=%v operation=%+v", s.Running, s.Operation)
	}
	calls := h.Calls()
	term := slices.IndexFunc(calls, func(c cmdrunner.Cmd) bool { return c.Name == "machinectl" && c.Args[0] == "terminate" })
	boot := slices.IndexFunc(calls, func(c cmdrunner.Cmd) bool { return c.Name == "systemd-run" })
	if term < 0 || term > boot {
		t.Errorf("the old container was ended at %d, the new one started at %d", term, boot)
	}
}
