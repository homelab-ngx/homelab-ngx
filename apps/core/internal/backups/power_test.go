package backups

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"homelab-ngx.com/core/backup-runner/runfile"
	"homelab-ngx.com/core/internal/cmdrunner"
)

type blockingStacks struct {
	fakeStacks
	started chan struct{}
	once    sync.Once
}

func (b *blockingStacks) RunPrepare(ctx context.Context, stack, unit string) error {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return ctx.Err()
}

func TestBeginShutdownRefusesTriggersAndCancelRestores(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	h.setState(succeed)
	b := h.backup("Nightly")

	h.svc.BeginShutdown()
	if _, err := h.svc.Trigger(ctx, b.Id, TriggerManual); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("trigger during shutdown = %v, want ErrShuttingDown", err)
	}
	code, m := call(t, h, "POST", "/api/backups/1/run", "", nil)
	if code != 409 || m["code"] != "shutting_down" {
		t.Fatalf("POST /api/backups/1/run = %d %v, want 409 shutting_down", code, m)
	}

	h.svc.CancelShutdown()
	if run := h.run(b.Id); run.Result != ResultSuccess {
		t.Fatalf("run after CancelShutdown = %+v", run)
	}
}

func TestRunningAndHaltStopsUnit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	b := h.backup("Nightly")

	h.setActive(true)
	var mu sync.Mutex
	var statePath string
	var stopCmd cmdrunnerCmd
	launched := make(chan struct{})
	h.fake.On("systemd-run", "--unit=homelab-backup-1").Do(func(c cmdrunnerCmd) (cmdrunnerResult, error) {
		specPath := argAfter(c.Args, "--spec")
		sp := argAfter(c.Args, "--state")
		_ = os.Remove(specPath)
		mu.Lock()
		statePath = sp
		mu.Unlock()
		close(launched)
		return cmdrunnerResult{}, nil
	})
	h.fake.On("systemctl", "stop", "homelab-backup-1.service").Do(func(c cmdrunnerCmd) (cmdrunnerResult, error) {
		mu.Lock()
		stopCmd = c
		sp := statePath
		mu.Unlock()
		if sp != "" {
			now := h.clock()
			_ = runfile.WriteState(sp, &runfile.State{
				Version: runfile.Version, RunID: 1, StartedAt: now, UpdatedAt: now, FinishedAt: &now,
				Result: runfile.ResultCancelled, Error: "the run was stopped",
			})
		}
		h.setActive(false)
		return cmdrunnerResult{}, nil
	})

	runID, err := h.svc.Trigger(ctx, b.Id, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	<-launched

	running, err := h.svc.Running(ctx)
	if err != nil || !slices.Equal(running, []string{"Nightly"}) {
		t.Fatalf("Running = %v %v, want [Nightly]", running, err)
	}
	if err := h.svc.Halt(ctx, "Nightly"); err != nil {
		t.Fatalf("Halt = %v", err)
	}
	if stopCmd.Name != "systemctl" || !slices.Contains(stopCmd.ExpectExit, 5) {
		t.Fatalf("stop cmd = %+v", stopCmd)
	}
	if running, err := h.svc.Running(ctx); err != nil || len(running) != 0 {
		t.Fatalf("Running after Halt = %v %v", running, err)
	}
	if row := h.runRow(runID); row.Result != ResultCancelled || row.Error != "the run was stopped" {
		t.Fatalf("run after Halt = %+v", row)
	}
}

func TestStopUnitAllowsUnloadedUnitExit5(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fake.On("systemctl", "stop", "homelab-backup-9.service").Do(func(cmdrunnerCmd) (cmdrunnerResult, error) {
		res := cmdrunnerResult{Cmd: "systemctl stop homelab-backup-9.service", ExitCode: 5}
		return res, &cmdrunner.Error{
			Result: res,
			Err:    errors.New("exit status 5"),
		}
	})
	if err := h.svc.stopUnit(context.Background(), "homelab-backup-9.service"); err != nil {
		t.Fatalf("stopUnit on exit 5 = %v, want nil", err)
	}
}

func TestHaltCancelsPrepareBeforeLaunch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	bs := &blockingStacks{started: make(chan struct{})}
	bs.folders = []StackFolders{{
		Stack: "immich", Running: true, Prepare: "dump.service",
		Folders: []StackFolder{{Key: "/dump", Label: "Dump", HostPath: filepath.Join(h.dir, "stacks/immich/root/dump")}},
	}}
	h.svc.stacks = bs
	d := h.dest(s3Input("Bucket"))
	b, err := h.svc.Create(ctx, BackupInput{
		Name: "PrepareTest", DestinationIds: []int64{d.Id},
		Sources: []SourceInput{{Kind: SourceStack, Stack: "immich", Folder: "/dump"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.fake.On("systemctl", "stop", "homelab-backup-1.service").Do(func(cmdrunnerCmd) (cmdrunnerResult, error) {
		return cmdrunnerResult{}, nil
	})

	runID, err := h.svc.Trigger(ctx, b.Id, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	<-bs.started

	if err := h.svc.Halt(ctx, "PrepareTest"); err != nil {
		t.Fatalf("Halt = %v", err)
	}
	if h.launchCount() != 0 {
		t.Fatalf("launched %d units, want 0", h.launchCount())
	}
	if row := h.runRow(runID); row.Result != ResultCancelled || row.Error != "the run was stopped" {
		t.Fatalf("run after cancelled prepare = %+v", row)
	}
}

func TestLaunchWantsPoolMounts(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.svc.cfg.PoolMounts = func() []string { return []string{"/var/mnt/pool", "/var/mnt/pool", ""} }
	h.setState(succeed)
	b := h.backup("Nightly")
	if r := h.run(b.Id); r.Result != ResultSuccess {
		t.Fatalf("run = %+v", r)
	}
	h.mu.Lock()
	args := strings.Join(h.launches[0].Args, "\x00")
	h.mu.Unlock()
	want := "WantsMountsFor=/var/mnt/pool"
	if count := strings.Count(args, want); count != 1 {
		t.Fatalf("count(%q) = %d in %v, want 1", want, count, h.launches[0].Args)
	}
}
