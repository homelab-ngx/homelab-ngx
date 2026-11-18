package services

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestListShowsStackUnitsSortedByName(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 10, 12, 8, 30, 0, 0, time.FixedZone("CEST", 2*3600))
	h := newHarness(t, []string{"caddy.service"},
		StackUnit{Stack: "media", Unit: "plex.service", Name: "media-plex.service", ActiveState: "active", SubState: "running", StartedAt: started},
		StackUnit{Stack: "email", Unit: "meilisearch.service", Name: "email-meilisearch.service"},
		// A stack unit named like a core unit is left to the core unit.
		StackUnit{Stack: "media", Unit: "caddy.service", Name: "caddy.service", ActiveState: "active"},
	)
	got := h.svc.List(bg)
	if len(got) != 2 || got[0].Name != "email-meilisearch" || got[1].Name != "media-plex" {
		t.Fatalf("list = %+v", got)
	}
	meili, plex := got[0], got[1]
	if meili.Stack != "email" || meili.UnitName != "email-meilisearch.service" || meili.ActiveState != "inactive" ||
		meili.SubState != "dead" || meili.StartedAt != nil || meili.Description != "In the email stack" {
		t.Fatalf("meilisearch = %+v", meili)
	}
	if plex.Type != Native || plex.ActiveState != "active" || plex.StartedAt == nil ||
		!plex.StartedAt.Equal(started) || plex.StartedAt.Location() != time.UTC {
		t.Fatalf("plex = %+v", plex)
	}
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), `"started_at":null`) {
		t.Fatal("started_at must serialise as null")
	}
	h.svc.SetStacks(nil)
	if empty := h.svc.List(bg); empty == nil || len(empty) != 0 {
		t.Fatal("no stacks must yield an empty non-nil slice")
	}
}

func TestStartStopRestartGoToTheStack(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"caddy.service"},
		StackUnit{Stack: "media", Unit: "plex.service", Name: "media-plex.service"})
	for _, f := range []func() error{
		func() error { return h.svc.Start(bg, "media-plex") },
		func() error { return h.svc.Stop(bg, "media-plex.service") },
		func() error { return h.svc.Restart(bg, "media-plex") },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(h.stacks.calls, ","); got != "start media-plex,stop media-plex.service,restart media-plex" {
		t.Fatalf("stack calls = %s", got)
	}
	// Core units are read-only, unknown names are unknown.
	for _, name := range []string{"caddy", "nope"} {
		if err := h.svc.Start(bg, name); !errors.Is(err, ErrUnknownUnit) {
			t.Fatalf("start %s: err = %v", name, err)
		}
	}
	h.stacks.err = errors.New("stack media is not running")
	if err := h.svc.Restart(bg, "media-plex"); err == nil || err.Error() != "stack media is not running" {
		t.Fatalf("stack error = %v", err)
	}
}

func TestCore(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"sshd.service", "caddy.service", "x.socket"})
	h.fake.On("systemctl", "show").Reply("Id=sshd.service\nActiveState=active\nSubState=running\nDescription=OpenSSH\n\nId=caddy.service\nActiveState=active\nSubState=running\nDescription=Caddy\n")
	got := h.svc.Core(bg)
	if len(got) != 3 || got[0].Name != "caddy" || got[1].Name != "sshd" || got[2].Name != "x" {
		t.Fatalf("core = %+v", got)
	}
	if got[0].Stack != "" || got[0].Description != "Caddy" || got[2].ActiveState != "inactive" {
		t.Fatalf("core[0] = %+v", got[0])
	}
	if empty := newHarness(t, nil).svc.Core(bg); empty == nil || len(empty) != 0 {
		t.Fatal("no core services must yield an empty non-nil slice")
	}
}
