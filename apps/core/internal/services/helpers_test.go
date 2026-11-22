package services

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/configstore"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/store"
	"homelab-ngx.com/core/internal/store/storetest"
	"homelab-ngx.com/core/internal/systemd"
)

var bg = context.Background()

// fakeStacks is the stacks package's side: units by host name, and the
// actions handed to it.
type fakeStacks struct {
	units []StackUnit
	err   error // returned by UnitAction
	calls []string
}

func (f *fakeStacks) Units(context.Context) []StackUnit { return f.units }

func (f *fakeStacks) UnitStack(_ context.Context, name string) (string, string, bool) {
	for _, u := range f.units {
		if u.Name == name || u.Name == name+".service" {
			return u.Stack, u.Unit, true
		}
	}
	return "", "", false
}

func (f *fakeStacks) UnitAction(_ context.Context, name, action string) error {
	f.calls = append(f.calls, action+" "+name)
	return f.err
}

func (f *fakeStacks) MaskUnit(_ context.Context, name string, mask bool) error {
	verb := "unmask"
	if mask {
		verb = "mask"
	}
	f.calls = append(f.calls, verb+" "+name)
	return f.err
}

type harness struct {
	t      *testing.T
	svc    *Service
	fake   *cmdrunnertest.Fake
	st     *store.Store
	cfg    *config.Manager
	stacks *fakeStacks
	now    time.Time
}

// newHarness builds a Service over a real store and config.Manager, a
// scripted command runner and fake stacks holding units.
func newHarness(t *testing.T, core []string, units ...StackUnit) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	doc := map[string]any{
		"version":       config.SupportedVersion,
		"db":            map[string]any{"snapshot_schedule": "15 3 * * *", "snapshot_keep": 7, "retention_days": 90},
		"core_services": core,
	}
	data, _ := json.Marshal(doc)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o444); err != nil {
		t.Fatal(err)
	}
	st, _ := storetest.Open(t)
	cfg := config.NewManager(path, configstore.New(st))
	if err := cfg.Load(ctx); err != nil {
		t.Fatal(err)
	}
	fake := cmdrunnertest.NewFake()
	h := &harness{t: t, fake: fake, st: st, cfg: cfg, stacks: &fakeStacks{units: units},
		now: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	var notify *notifications.Notifier
	h.svc = New(systemd.New(fake, systemd.WithPollInterval(time.Millisecond)), cfg, notify,
		slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	h.svc.SetStacks(h.stacks)
	h.svc.now = func() time.Time { return h.now }
	return h
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }
