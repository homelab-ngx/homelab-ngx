package stacks

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/store"
)

const mlUnit = "immich-ml.service"

// twoUnitManifest is immich with a second service in its health check.
var twoUnitManifest = strings.Replace(immichManifest, `"units": ["immich-server.service"]`,
	`"units": ["immich-server.service", "immich-ml.service"]`, 1)

// newTwoUnits returns immich installed with two services and running.
func newTwoUnits(t *testing.T) (*Service, *fakeSource, *fakeHost) {
	t.Helper()
	svc, fs, h := newServiceOn(t)
	fs.set("aaaaaaa1", twoUnitManifest)
	ctx := context.Background()
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(ctx, "immich", "aaaaaaa1"); err != nil {
		t.Fatal(err)
	}
	if s := finished(t, svc, svc.Start); s.Operation.Error != "" {
		t.Fatalf("start: %s", s.Operation.Error)
	}
	return svc, fs, h
}

// masked reports which of immich's units Units shows disabled.
func masked(t *testing.T, svc *Service) []string {
	t.Helper()
	svc.mu.Lock()
	svc.unitsAt = svc.unitsAt.AddDate(-1, 0, 0)
	svc.mu.Unlock()
	var out []string
	for _, u := range svc.Units(context.Background()) {
		if u.Masked {
			out = append(out, u.Unit)
		}
	}
	return out
}

// since returns a function that lists the calls made after now whose argv
// starts with prefix.
func since(h *fakeHost) func(prefix ...string) []cmdrunner.Cmd {
	n := len(h.Calls())
	return func(prefix ...string) []cmdrunner.Cmd {
		var out []cmdrunner.Cmd
		for _, c := range h.Calls()[n:] {
			if slices.Equal(append([]string{c.Name}, c.Args...)[:min(len(prefix), len(c.Args)+1)], prefix) {
				out = append(out, c)
			}
		}
		return out
	}
}

func lastBoot(t *testing.T, h *fakeHost) string {
	t.Helper()
	boots := h.CallsMatching("systemd-run", "--unit=homelab-stack-immich")
	if len(boots) == 0 {
		t.Fatal("never booted")
	}
	return argv(boots[len(boots)-1])
}

func TestNspawnArgsBindTheDisabledServicesOnlyWhenThereAreSome(t *testing.T) {
	t.Parallel()
	m, err := ParseManifest([]byte(twoUnitManifest))
	if err != nil {
		t.Fatal(err)
	}
	without := nspawnArgs("immich", "/s", "/p", "/v", "", m, nil)
	with := nspawnArgs("immich", "/s", "/p", "/v", "/run/homelab-stack-units/immich", m, nil)
	bind := "--bind-ro=/run/homelab-stack-units/immich:/run/systemd/system.control"
	if slices.ContainsFunc(without, isMaskBind) || !slices.Contains(with, bind) {
		t.Fatalf("without: %v\nwith: %v", without, with)
	}
	// The init path stays last.
	if with[len(with)-1] != stackProfile+"/system/init" {
		t.Errorf("last argument = %q", with[len(with)-1])
	}
	// Disabling or enabling a service takes effect in the running stack,
	// so the bind alone is no reason to restart it.
	if !sameBoot(without, with) {
		t.Error("the mask bind made a restart needed")
	}
	if sameBoot(without, nspawnArgs("immich", "/s", "/p", "/v", "", m, []hostBind{{"/pool/x", "/x", false}})) {
		t.Error("sameBoot ignores a new host bind")
	}
}

func TestDisablingAServiceStopsItAndKeepsItStopped(t *testing.T) {
	t.Parallel()
	svc, _, h := newTwoUnits(t)
	ctx := context.Background()
	calls := since(h)

	if err := svc.MaskUnit(ctx, "immich-ml", true); err != nil {
		t.Fatal(err)
	}
	// Booted without the bind: the link goes into the stack's own /run,
	// then systemd reloads and the unit stops.
	var got []string
	for _, c := range calls() {
		got = append(got, argv(c))
	}
	want := []string{
		"systemd-run --machine=immich --wait --collect --quiet -- /nix/var/nix/profiles/system/sw/bin/mkdir -p -- /run/systemd/system.control",
		"systemd-run --machine=immich --wait --collect --quiet -- /nix/var/nix/profiles/system/sw/bin/ln -sfn -- /dev/null /run/systemd/system.control/immich-ml.service",
		"systemctl --machine=immich daemon-reload",
		"systemctl --machine=immich stop -- immich-ml.service",
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, strings.Join(got, "\n"))
		}
	}
	if l, err := os.Readlink(filepath.Join(svc.unitsDir("immich"), mlUnit)); err != nil || l != "/dev/null" {
		t.Errorf("mask link = %q, %v", l, err)
	}
	if m := masked(t, svc); !slices.Equal(m, []string{mlUnit}) {
		t.Errorf("masked units = %v", m)
	}
	// Disabling it again changes nothing.
	calls = since(h)
	if err := svc.MaskUnit(ctx, "immich-ml.service", true); err != nil || len(calls("systemctl", "--machine=immich", "stop")) != 0 {
		t.Errorf("second disable: %v, calls %v", err, calls())
	}

	// It cannot be started or restarted; it can be stopped.
	wantCode(t, svc.UnitAction(ctx, "immich-ml", "start"), 409, "unit_masked")
	wantCode(t, svc.UnitAction(ctx, "immich-ml", "restart"), 409, "unit_masked")
	if err := svc.UnitAction(ctx, "immich-ml", "stop"); err != nil {
		t.Errorf("stop: %v", err)
	}

	// Health, the monitor and the metrics leave it out.
	h.deadUnits[mlUnit] = true
	r, _ := svc.row(ctx, "immich")
	if err := svc.probeStack(ctx, r); err != nil {
		t.Errorf("health check of a stack with a stopped, disabled unit: %v", err)
	}
	for _, m := range svc.Measured(ctx) {
		if !slices.Equal(m.Units, []string{"immich-server.service"}) {
			t.Errorf("measured units = %v", m.Units)
		}
	}
	if s, _ := svc.Get(ctx, "immich"); s.RestartNeeded {
		t.Error("disabling a service asks for a restart")
	}

	// Across a restart: the next boot binds the masks, and the stack is
	// healthy with the unit stopped.
	finished(t, svc, svc.Stop)
	s := finished(t, svc, svc.Start)
	if s.Operation.Error != "" || !s.Running {
		t.Fatalf("restart: %+v", s.Operation)
	}
	if b := lastBoot(t, h); !strings.Contains(b, "--bind-ro="+svc.unitsDir("immich")+":/run/systemd/system.control") {
		t.Errorf("boot has no mask bind: %s", b)
	}
	if m := masked(t, svc); !slices.Equal(m, []string{mlUnit}) {
		t.Errorf("masked units after a restart = %v", m)
	}
}

func TestEnablingAServiceStartsNothing(t *testing.T) {
	t.Parallel()
	svc, _, h := newTwoUnits(t)
	ctx := context.Background()
	if err := svc.MaskUnit(ctx, "immich-ml", true); err != nil {
		t.Fatal(err)
	}
	finished(t, svc, svc.Stop)
	finished(t, svc, svc.Start) // now booted with the bind
	calls := since(h)

	if err := svc.MaskUnit(ctx, "immich-ml", false); err != nil {
		t.Fatal(err)
	}
	// The bound directory changed on the host: a reload is all it takes.
	if n := len(calls("systemd-run", "--machine=immich")); n != 0 {
		t.Errorf("wrote into the stack %d times although its masks are bound", n)
	}
	if n := len(calls("systemctl", "--machine=immich", "daemon-reload")); n != 1 {
		t.Errorf("reloads = %d", n)
	}
	if n := len(calls("systemctl", "--machine=immich", "start")); n != 0 {
		t.Error("enabling started the unit")
	}
	if entries, _ := os.ReadDir(svc.unitsDir("immich")); len(entries) != 0 {
		t.Errorf("mask links left: %v", entries)
	}
	if m := masked(t, svc); len(m) != 0 {
		t.Errorf("masked units = %v", m)
	}
	if err := svc.UnitAction(ctx, "immich-ml", "start"); err != nil {
		t.Errorf("start after enabling: %v", err)
	}
	if s, _ := svc.Get(ctx, "immich"); s.RestartNeeded {
		t.Error("enabling a service asks for a restart")
	}
	// The next boot has no bind.
	finished(t, svc, svc.Stop)
	finished(t, svc, svc.Start)
	if b := lastBoot(t, h); strings.Contains(b, stackMaskDir) {
		t.Errorf("boot still binds the masks: %s", b)
	}
}

func TestTheLastEnabledServiceCannotBeDisabled(t *testing.T) {
	t.Parallel()
	svc, _, _ := newTwoUnits(t)
	ctx := context.Background()
	if err := svc.MaskUnit(ctx, "immich-ml", true); err != nil {
		t.Fatal(err)
	}
	wantCode(t, svc.MaskUnit(ctx, "immich-server", true), 409, "last_unit")

	one, _, _ := newRuntime(t)
	finished(t, one, one.Start)
	wantCode(t, one.MaskUnit(ctx, "immich-server", true), 409, "last_unit")
	wantCode(t, one.MaskUnit(ctx, "immich-nope", true), 404, "unknown_unit")
}

func TestDisablingAServiceOfAStoppedStackOnlyStoresIt(t *testing.T) {
	t.Parallel()
	svc, _, h := newTwoUnits(t)
	ctx := context.Background()
	finished(t, svc, svc.Stop)
	calls := since(h)
	if err := svc.MaskUnit(ctx, "immich-ml", true); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls() {
		if slices.Contains(c.Args, "--machine=immich") {
			t.Errorf("called into a stopped stack: %s", argv(c))
		}
	}
	if m := masked(t, svc); !slices.Equal(m, []string{mlUnit}) {
		t.Errorf("masked units = %v", m)
	}
	l, err := svc.CurrentLock(ctx)
	if err != nil || len(l.Stacks) != 1 || !slices.Equal(l.Stacks[0].MaskedUnits, []string{mlUnit}) {
		t.Errorf("lock = %+v, %v", l, err)
	}
}

func TestAnUpdateKeepsADisabledService(t *testing.T) {
	t.Parallel()
	svc, fs, h := newTwoUnits(t)
	ctx := context.Background()
	if err := svc.MaskUnit(ctx, "immich-ml", true); err != nil {
		t.Fatal(err)
	}
	h.deadUnits[mlUnit] = true
	release(t, svc, fs, "bbbbbbb2", strings.Replace(twoUnitManifest, `"port": 2283, "max_body": "50GB"`, `"port": 3001`, 1))
	s := finished(t, svc, svc.Update)
	if s.Operation.Error != "" || s.Generation != 2 {
		t.Fatalf("update: generation %d, %+v", s.Generation, s.Operation)
	}
	// Switched in place: the mask written into the running stack holds.
	if n := len(h.CallsMatching("systemd-run", "--unit=homelab-stack-immich")); n != 1 {
		t.Errorf("booted %d times; the update should switch in place", n)
	}
	if m := masked(t, svc); !slices.Equal(m, []string{mlUnit}) {
		t.Errorf("masked units after the update = %v", m)
	}

	// A manifest that drops the unit drops its mask with it.
	release(t, svc, fs, "ccccccc3", immichManifest)
	if s := finished(t, svc, svc.Update); s.Operation.Error != "" {
		t.Fatalf("update: %+v", s.Operation)
	}
	if m := masked(t, svc); len(m) != 0 {
		t.Errorf("masked units = %v", m)
	}
	finished(t, svc, svc.Stop)
	finished(t, svc, svc.Start)
	if b := lastBoot(t, h); strings.Contains(b, stackMaskDir) {
		t.Errorf("a mask of a unit the manifest dropped is still bound: %s", b)
	}
}

func TestAFrozenStacksServiceIsNotDisabled(t *testing.T) {
	t.Parallel()
	svc, _, h := newTwoUnits(t)
	h.frozen = true
	wantCode(t, svc.MaskUnit(context.Background(), "immich-ml", true), 409, "stack_frozen")
	if m := storedMasks(mustRow(t, svc)); len(m) != 0 {
		t.Errorf("stored masks = %v", m)
	}
}

func mustRow(t *testing.T, svc *Service) store.Stack {
	t.Helper()
	r, err := svc.row(context.Background(), "immich")
	if err != nil {
		t.Fatal(err)
	}
	return r
}
