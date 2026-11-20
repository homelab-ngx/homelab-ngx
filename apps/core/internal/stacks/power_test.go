package stacks

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/httpx"
)

func TestHaltStopsTheContainerAndRecordsNothing(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	finished(t, svc, svc.Start)
	ctx := context.Background()
	if names, err := svc.Running(ctx); err != nil || !slices.Equal(names, []string{"immich"}) {
		t.Fatalf("running = %v, %v", names, err)
	}
	if err := svc.Halt(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.CallsMatching("systemctl", "stop", unitName("immich"))); n != 1 {
		t.Errorf("unit stops = %d", n)
	}
	s, err := svc.Get(ctx, "immich")
	if err != nil {
		t.Fatal(err)
	}
	if !s.WantRunning {
		t.Error("a halt for a shutdown made the stack not want to run")
	}
	// Rules and routes go with the reboot: a halt leaves them.
	if n := len(h.CallsMatching("nft", "destroy", "table", "inet", "homelab_stack_immich")); n != 0 {
		t.Errorf("rule removals = %d", n)
	}
}

func TestAFrozenServiceStartsNothing(t *testing.T) {
	t.Parallel()
	svc, _, _ := newRuntime(t)
	ctx := context.Background()
	svc.Freeze()
	_, err := svc.Start(ctx, "immich")
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != "shutting_down" {
		t.Fatalf("start while frozen: %v", err)
	}
	if err := svc.boot(ctx, "immich", "", Manifest{}); !errors.As(err, &p) || p.Code != "shutting_down" {
		t.Fatalf("boot while frozen: %v", err)
	}
	svc.Thaw()
	finished(t, svc, svc.Start)
}

func TestBusyNamesOperationsButNotHealthChecks(t *testing.T) {
	t.Parallel()
	svc, _, _ := newRuntime(t)
	now := time.Now()
	svc.mu.Lock()
	svc.ops["immich"] = &operation{kind: opUpdate, phase: "building", started: now}
	svc.ops["players"] = &operation{kind: opStart, phase: "starting", started: now, check: true}
	svc.ops["arr"] = &operation{kind: opStop, phase: "stopped", started: now, finished: now}
	svc.mu.Unlock()
	if got := svc.Busy(); !slices.Equal(got, []string{"stack immich: update (building)"}) {
		t.Fatalf("busy = %v", got)
	}
}

func TestUnitPropertiesOrderTheStopAndRestartNothing(t *testing.T) {
	t.Parallel()
	m := mustParse(t, v5Manifest(photosPath))
	b, err := hostBinds(m, []storedVar{{Name: "PHOTOS", Value: "/var/mnt/media/photos"}})
	if err != nil {
		t.Fatal(err)
	}
	a := nspawnArgs("immich", "/state/immich", "/profiles/immich", "/vars/immich", m, b)
	if !slices.Contains(a, "WantsMountsFor=/var/mnt/media/photos") || !slices.Contains(a, "TimeoutStopSec=120") {
		t.Fatalf("args = %v", a)
	}
	if slices.Index(a, "WantsMountsFor=/var/mnt/media/photos") > slices.Index(a, "systemd-nspawn") {
		t.Fatalf("a unit property after systemd-nspawn: %v", a)
	}
	// A container booted before the properties existed is the same boot.
	i := slices.Index(a, "systemd-nspawn")
	old := append([]string{"--unit=homelab-stack-immich", "-p", "Type=notify"}, a[i:]...)
	if !sameBoot(old, a) {
		t.Error("unit properties alone ask for a restart")
	}
	changed := append(slices.Clone(a), "--bind=/x:/x:idmap")
	if sameBoot(a, changed) {
		t.Error("a new bind does not ask for a restart")
	}
}
