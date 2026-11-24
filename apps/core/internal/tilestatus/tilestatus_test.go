package tilestatus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func fixed(m map[string]TileStatus) Reporter {
	return func(context.Context) (map[string]TileStatus, error) { return m, nil }
}

func TestCollectMergesReporters(t *testing.T) {
	g := NewRegistry(nil)
	g.Add("a", fixed(map[string]TileStatus{"clock": Of(Warn), "lan": Of(Off)}))
	g.Add("b", fixed(map[string]TileStatus{"wireguard": Counted(OK, 2), "empty": {}}))
	got := g.Collect(context.Background())
	if len(got) != 3 || got["clock"].State != Warn || got["lan"].State != Off || *got["wireguard"].Count != 2 {
		t.Fatalf("got %+v", got)
	}
	if _, ok := got["empty"]; ok {
		t.Fatal("a status with neither state nor count should be dropped")
	}
}

func TestCollectFirstReporterWins(t *testing.T) {
	g := NewRegistry(nil)
	g.Add("a", fixed(map[string]TileStatus{"domain": Of(Bad)}))
	g.Add("b", fixed(map[string]TileStatus{"domain": Of(Off)}))
	if s := g.Collect(context.Background())["domain"]; s.State != Bad {
		t.Fatalf("got %+v", s)
	}
}

func TestCollectSkipsFailures(t *testing.T) {
	g := NewRegistry(nil)
	g.timeout = 50 * time.Millisecond
	g.Add("err", func(context.Context) (map[string]TileStatus, error) {
		return map[string]TileStatus{"x": Of(OK)}, errors.New("boom")
	})
	g.Add("panic", func(context.Context) (map[string]TileStatus, error) { panic("boom") })
	g.Add("slow", func(ctx context.Context) (map[string]TileStatus, error) {
		time.Sleep(time.Second) // ignores its context: the registry must not wait
		return map[string]TileStatus{"slow": Of(OK)}, nil
	})
	g.Add("ok", fixed(map[string]TileStatus{"ok": Of(OK)}))
	start := time.Now()
	got := g.Collect(context.Background())
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("waited %v for a slow reporter", d)
	}
	if len(got) != 1 || got["ok"].State != OK {
		t.Fatalf("got %+v", got)
	}
}

func TestCollectRunsConcurrently(t *testing.T) {
	g := NewRegistry(nil)
	for _, k := range []string{"a", "b", "c", "d"} {
		g.Add(k, func(context.Context) (map[string]TileStatus, error) {
			time.Sleep(200 * time.Millisecond)
			return map[string]TileStatus{k: Of(OK)}, nil
		})
	}
	start := time.Now()
	got := g.Collect(context.Background())
	if d := time.Since(start); d > 600*time.Millisecond {
		t.Fatalf("reporters ran one after another: %v", d)
	}
	if len(got) != 4 {
		t.Fatalf("got %+v", got)
	}
}

func TestStatusJSON(t *testing.T) {
	b, _ := json.Marshal(SettingsStatus{Tiles: map[string]TileStatus{"firewall": {Count: ptr(3)}, "clock": Of(Warn)}})
	want := `{"tiles":{"clock":{"state":"warn"},"firewall":{"count":3}}}`
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
}

func ptr(n int) *int { return &n }
