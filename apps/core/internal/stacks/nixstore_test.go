package stacks

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
)

func init() { collectDelay = time.Millisecond }

func TestAStackKeepsTheGenerationItRunsAndTwoBefore(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	finished(t, svc, svc.Start)
	var s Stack
	for i := 2; i <= 5; i++ {
		rev := fmt.Sprintf("bbbbbbb%d", i)
		release(t, svc, fs, rev, strings.Replace(immichManifest, `"port": 2283`, fmt.Sprintf(`"port": %d`, 3000+i), 1))
		if s = finished(t, svc, svc.Update); s.Operation.Error != "" {
			t.Fatalf("update %d: %s", i, s.Operation.Error)
		}
	}
	var ids []int
	for _, g := range s.Generations {
		ids = append(ids, g.Id)
	}
	if fmt.Sprint(ids) != "[5 4 3]" {
		t.Errorf("generations = %v, want the current one and two before", ids)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gens[0] != "" || h.gens[1] != "" || h.gens[2] == "" || h.current != 5 {
		t.Errorf("profile = %q, current %d", h.gens, h.current)
	}
}

func TestARollbackKeepsTheGenerationItLeft(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newRuntime(t)
	finished(t, svc, svc.Start)
	release(t, svc, fs, "bbbbbbb2", strings.Replace(immichManifest, `"port": 2283`, `"port": 3002`, 1))
	finished(t, svc, svc.Update)
	s := finished(t, svc, func(ctx context.Context, n string) (Stack, error) { return svc.Rollback(ctx, n, 0) })
	if s.Generation != 1 || len(s.Generations) != 2 {
		t.Errorf("generation = %d, generations = %+v", s.Generation, s.Generations)
	}
}

func TestRemovingAStackCollectsAndMeasuresTheStore(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	dir := t.TempDir()
	svc.host.StoreDir, svc.host.StoreFile = dir, filepath.Join(dir, "state", "nix-store.json")
	var free atomic.Uint64
	free.Store(10 << 30)
	svc.statfs = func(string) (uint64, uint64, error) { return 100 << 30, free.Load(), nil }
	h.On("nix-collect-garbage").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		free.Store(13 << 30)
		return ok("")
	})
	h.On("du").Reply("12345\t" + dir + "\n")

	if err := svc.Remove(context.Background(), "immich"); err != nil {
		t.Fatal(err)
	}
	var st NixStore
	for range 500 {
		if st = svc.NixStore(context.Background()); st.MeasuredAt != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.CollectedAt == nil || st.FreedBytes != 3<<30 || st.UsedBytes != 12345 || st.FreeBytes != 13<<30 || st.TotalBytes != 100<<30 {
		t.Fatalf("store = %+v", st)
	}
	// The next daemon knows it too.
	svc.storeMu.Lock()
	svc.storeRec = nil
	svc.storeMu.Unlock()
	if again := svc.NixStore(context.Background()); again.UsedBytes != 12345 || again.CollectedAt == nil {
		t.Errorf("after a restart: %+v", again)
	}
}

func TestACollectionWaitsForBuildsAndBuildsForIt(t *testing.T) {
	t.Parallel()
	g := newStoreGate()
	started, finish := make(chan struct{}), make(chan struct{})
	var runs atomic.Int32
	collect := func() {
		runs.Add(1)
		started <- struct{}{}
		<-finish
	}
	release := g.hold()
	g.request(collect)
	g.request(collect)
	select {
	case <-started:
		t.Fatal("collected while a build ran")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	<-started
	held := make(chan struct{})
	go func() { defer g.hold()(); close(held) }()
	select {
	case <-held:
		t.Fatal("a build started during the collection")
	case <-time.After(50 * time.Millisecond):
	}
	finish <- struct{}{}
	<-held
	time.Sleep(50 * time.Millisecond)
	if n := runs.Load(); n != 1 || g.busy() {
		t.Errorf("runs = %d, busy = %v; two requests before the run are one run", n, g.busy())
	}
}
