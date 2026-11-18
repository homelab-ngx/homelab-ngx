package stacks

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func readLockFile(t *testing.T, svc *Service) Lock {
	t.Helper()
	b, err := os.ReadFile(svc.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	var l Lock
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestRestoreOntoAnEmptyStackTableBringsTheStackBack(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	ctx := context.Background()
	before := finished(t, svc, svc.Start)
	if _, err := svc.SetVariable(ctx, "immich", "OWN", StackVariableSet{Value: "kept"}); err != nil {
		t.Fatal(err)
	}
	svc.monitorOnce(ctx, map[string]int{}) // want_running reaches the lock
	l := readLockFile(t, svc)
	if len(l.Stacks) != 1 || l.Stacks[0].Rev != "aaaaaaa1" || !l.Stacks[0].WantRunning || l.Stacks[0].Subnet != "10.99.0.0/24" {
		t.Fatalf("lock = %+v", l)
	}

	// The daemon's database is lost; the state directory is not.
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
	q := svc.st.Queries()
	if err := q.DeleteStackGenerations(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.DeleteStack(ctx, "immich"); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Restore(ctx, l)
	if err != nil || len(res.Restored) != 1 || len(res.Skipped) != 0 {
		t.Fatalf("restore = %+v, %v", res, err)
	}
	svc.mu.Lock()
	o := svc.ops["immich"]
	svc.mu.Unlock()
	if o == nil {
		t.Fatal("a stack that was running was not started")
	}
	select {
	case <-o.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("start did not finish; phase %q", o.phase)
	}
	after, err := svc.Get(ctx, "immich")
	if err != nil {
		t.Fatal(err)
	}
	if after.Approved == nil || after.Approved.Rev != "aaaaaaa1" ||
		after.Address != before.Address || !after.Running || after.Generation == 0 || len(after.NewGrants) != 0 {
		t.Errorf("restored stack = %+v", after)
	}
	// The values come back with the state directory.
	if v := varByName(t, after, "OWN"); v.Value != "kept" {
		t.Errorf("restored variables = %+v", after.Variables)
	}

	again, err := svc.Restore(ctx, l)
	if err != nil || len(again.Restored) != 0 || !strings.Contains(again.Skipped["immich"], "already installed") {
		t.Errorf("second restore = %+v, %v", again, err)
	}
}

func TestRestoreRefusesABadLock(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	ctx := context.Background()
	_, err := svc.Restore(ctx, Lock{Version: 9})
	wantCode(t, err, 422, "invalid_lock")

	res, err := svc.Restore(ctx, Lock{Version: LockVersion, Stacks: []LockedStack{
		{Name: "Media", Source: "github:o/media", Rev: "r", URL: "github:o/media/r", Manifest: json.RawMessage(immichManifest)},
	}})
	if err != nil || !strings.Contains(res.Skipped["Media"], "is not a stack name") {
		t.Errorf("restore = %+v, %v", res, err)
	}
}

func TestUnapprovedStacksAreNotLocked(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	fs.set("aaaaaaa1", immichManifest)
	if _, err := svc.Add(context.Background(), src, ""); err != nil {
		t.Fatal(err)
	}
	if l := readLockFile(t, svc); len(l.Stacks) != 0 {
		t.Errorf("lock = %+v", l)
	}
}

func TestCheckNotifiesOncePerStackWithCommitsWaiting(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	ctx := context.Background()
	finished(t, svc, svc.Start)
	open := func() map[string]string {
		h.mu.Lock()
		defer h.mu.Unlock()
		out := map[string]string{}
		for k, v := range h.problems {
			if strings.HasPrefix(k, updateScope) {
				out[k] = v
			}
		}
		return out
	}

	if _, err := svc.CheckUpdates(ctx); err != nil || len(open()) != 0 {
		t.Fatalf("up to date: problems %v, %v", open(), err)
	}

	fs.set("bbbbbbb2", immichManifest) // same grants: approved by the check
	if _, err := svc.CheckUpdates(ctx); err != nil {
		t.Fatal(err)
	}
	if p := open()["stack-update:immich"]; !strings.Contains(p, "Stack immich has an update:") || !strings.Contains(p, "homelab stacks update immich") {
		t.Errorf("approved update: %v", open())
	}

	fs.set("ccccccc3", strings.Replace(immichManifest, `"egress": [{"to": "host", "port": 25}]`, `"egress": [{"to": "host", "port": 25}, {"to": "192.168.1.102/32", "port": 1883}]`, 1))
	if _, err := svc.CheckUpdates(ctx); err != nil {
		t.Fatal(err)
	}
	if p := open()["stack-update:immich"]; !strings.Contains(p, "needs approval") || !strings.Contains(p, "192.168.1.102/32 port 1883/tcp") {
		t.Errorf("update with new grants: %v", open())
	}

	if _, err := svc.Approve(ctx, "immich", "ccccccc3"); err != nil {
		t.Fatal(err)
	}
	finished(t, svc, svc.Update)
	svc.monitorOnce(ctx, map[string]int{})
	if len(open()) != 0 {
		t.Errorf("after the update: %v", open())
	}
}
