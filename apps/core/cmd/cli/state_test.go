package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"filippo.io/age"

	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/store/storetest"
)

// TestMain points the CLI at a test age key.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cli-age")
	if err != nil {
		panic(err)
	}
	id, _ := age.GenerateX25519Identity()
	ageKeyFile = filepath.Join(dir, "keys.txt")
	if err := os.WriteFile(ageKeyFile, []byte(id.String()+"\n"), 0o600); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// hook is a webhook receiver counting the notifications it took with the token.
func hook(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	var got atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer tok" {
			got.Add(1)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &got
}

// The CLI delivers through the channels in the daemon's DB, without
// migrating it, and records the problem there.
func TestNotifierUsesTheDaemonsChannelsAndDB(t *testing.T) {
	t.Parallel()
	daemon, path := storetest.Open(t)
	url, got := hook(t)
	ctx := context.Background()
	if _, err := notifications.NewChannels(daemon, ageKeyFile).Create(ctx, notifications.ChannelInput{
		Name: "hook", Kind: notifications.KindWebhook, URL: url, Token: "tok",
	}); err != nil {
		t.Fatal(err)
	}
	n := notifierFor(ctx, &config.Nix{Paths: config.Paths{DB: path}})
	if _, err := n.Report(ctx, notifications.Problem{Key: "unit:x", Title: "x failing"}); err != nil {
		t.Fatal(err)
	}
	if res, _ := n.Report(ctx, notifications.Problem{Key: "unit:x", Title: "x failing"}); len(res.Opened) != 0 {
		t.Fatalf("second report must dedup: %+v", res)
	}
	rows, err := daemon.Queries().ListNotificationProblems(ctx)
	if err != nil || len(rows) != 1 || rows[0].Key != "unit:x" || got.Load() != 1 {
		t.Fatalf("problems %+v err %v delivered %d", rows, err, got.Load())
	}
}

func TestNotifierDisabledWithoutTheDB(t *testing.T) {
	t.Parallel()
	n := notifierFor(context.Background(), &config.Nix{Paths: config.Paths{DB: filepath.Join(t.TempDir(), "missing", "x.db")}})
	if n.Enabled() {
		t.Fatal("enabled without channels")
	}
}
