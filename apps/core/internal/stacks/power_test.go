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
	svc.BeginShutdown()
	_, err := svc.Start(ctx, "immich")
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != "shutting_down" {
		t.Fatalf("start while shutting down: %v", err)
	}
	if err := svc.boot(ctx, "immich", "", Manifest{}); !errors.As(err, &p) || p.Code != "shutting_down" {
		t.Fatalf("boot while shutting down: %v", err)
	}
	svc.CancelShutdown()
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
	b, err := hostBinds(m, []storedVar{{Name: "PHOTOS", Value: "/var/mnt/media/photos"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := nspawnArgs("immich", "/state/immich", "/profiles/immich", "/vars/immich", "", m, b)
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

func TestHoldupNamesTheUnitKilledLast(t *testing.T) {
	t.Parallel()
	svc, _, h := newServiceOn(t)
	ctx := context.Background()
	// The container began stopping at 1000 and is killed 120 s on.
	h.On("systemctl", "show", "--property=Id,Description,ActiveState,StateChangeTimestamp,TimeoutStopUSec", "--timestamp=unix", "--", unitName("players")).
		Reply("Id=homelab-stack-players.service\nDescription=Stack players\nActiveState=deactivating\nStateChangeTimestamp=@1000\nTimeoutStopUSec=2min\n")
	h.On("systemctl", "--machine=players", "list-units").
		Reply("plex.service loaded deactivating final-sigterm Plex Media Server\ndb.service loaded deactivating stop-sigterm DB\n")
	h.On("systemctl", "--machine=players", "show").
		Reply("Id=plex.service\nDescription=Plex Media Server\nActiveState=deactivating\nStateChangeTimestamp=@1001\nTimeoutStopUSec=1min 30s\n\n" +
			"Id=db.service\nDescription=DB\nActiveState=deactivating\nStateChangeTimestamp=@1000\nTimeoutStopUSec=10s\n")
	unit, desc, at, ok := svc.Holdup(ctx, "players")
	if !ok || unit != "plex.service" || desc != "Plex Media Server" || !at.Equal(time.Unix(1091, 0)) {
		t.Fatalf("holdup = %q %q %v %v", unit, desc, at, ok)
	}
}

func TestHoldupIsCappedByTheContainersKill(t *testing.T) {
	t.Parallel()
	svc, _, h := newServiceOn(t)
	h.On("systemctl", "show").
		Reply("Id=homelab-stack-players.service\nActiveState=deactivating\nStateChangeTimestamp=@1000\nTimeoutStopUSec=2min\n")
	h.On("systemctl", "--machine=players", "list-units").Reply("slow.service loaded deactivating stop Slow\n")
	h.On("systemctl", "--machine=players", "show").
		Reply("Id=slow.service\nDescription=Slow\nActiveState=deactivating\nStateChangeTimestamp=@1000\nTimeoutStopUSec=infinity\n")
	unit, _, at, ok := svc.Holdup(context.Background(), "players")
	if !ok || unit != "slow.service" || !at.Equal(time.Unix(1120, 0)) {
		t.Fatalf("holdup = %q %v %v", unit, at, ok)
	}
}

func TestHoldupFallsBackToTheContainer(t *testing.T) {
	t.Parallel()
	svc, _, h := newServiceOn(t)
	h.On("systemctl", "show").
		Reply("Id=homelab-stack-players.service\nActiveState=deactivating\nStateChangeTimestamp=@1000\nTimeoutStopUSec=2min\n")
	h.On("systemctl", "--machine=players", "list-units").Fail("Failed to connect to bus")
	unit, _, at, ok := svc.Holdup(context.Background(), "players")
	if !ok || unit != unitName("players") || !at.Equal(time.Unix(1120, 0)) {
		t.Fatalf("holdup = %q %v %v", unit, at, ok)
	}
}

func TestHoldupOfAStackNotStopping(t *testing.T) {
	t.Parallel()
	svc, _, h := newServiceOn(t)
	h.On("systemctl", "show").
		Reply("Id=homelab-stack-players.service\nActiveState=active\nStateChangeTimestamp=@1000\nTimeoutStopUSec=2min\n")
	if _, _, _, ok := svc.Holdup(context.Background(), "players"); ok {
		t.Fatal("a running stack has a holdup")
	}
}

func TestTimespan(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]time.Duration{
		"1min 30s": 90 * time.Second, "2min": 2 * time.Minute, "500ms": 500 * time.Millisecond,
		"1h 2min 3.5s": time.Hour + 2*time.Minute + 3500*time.Millisecond,
	} {
		if got, ok := timespan(in); !ok || got != want {
			t.Errorf("timespan(%q) = %v, %v", in, got, ok)
		}
	}
	for _, in := range []string{"infinity", "", "5 parsecs", "s"} {
		if _, ok := timespan(in); ok {
			t.Errorf("timespan(%q) read", in)
		}
	}
}
