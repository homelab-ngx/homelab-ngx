package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner"
)

func TestMountedListsOursDeepestFirst(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	info := filepath.Join(t.TempDir(), "mountinfo")
	lines := "" +
		"22 1 259:2 / / ro - erofs /dev/dm-0 ro\n" +
		"30 22 259:8 / /var rw - ext4 /dev/nvme0n1p8 rw\n" +
		"41 30 8:0 / /var/mnt/media rw - bcachefs /dev/sda rw\n" +
		"42 41 8:16 / /var/mnt/media/old\\040photos rw - ext4 /dev/sdb1 rw\n" +
		"43 30 8:32 / /var/mnt/cache rw - ext4 /dev/sdc1 rw\n" +
		"44 30 0:50 / /var/lib/machines rw - tmpfs tmpfs rw\n"
	if err := os.WriteFile(info, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	got := h.svc.mountedIn(info)
	want := []string{"/var/mnt/media/old photos", "/var/mnt/media", "/var/mnt/cache"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mounted = %q, want %q", got, want)
	}
}

func TestReleaseRecordsNothingAndTriesABusyMountAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newPartHarness(t, "/var/mnt/cache")
	h.fake.On("chattr").Reply("")
	tries, stuck := 0, false
	h.fake.On("umount").Do(func(_ cmdrunner.Cmd) (cmdrunner.Result, error) {
		tries++
		if stuck || tries < 2 {
			return cmdrunner.Result{ExitCode: 32}, errors.New("umount: /var/mnt/cache: target is busy")
		}
		return cmdrunner.Result{}, nil
	})
	if err := h.svc.release(ctx, "/var/mnt/cache", 0); err != nil {
		t.Fatal(err)
	}
	if tries != 2 {
		t.Fatalf("umount tries = %d", tries)
	}
	if got, want := chattrs(h.harness), []string{"+i /var/mnt/cache"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chattr calls = %v, want %v", got, want)
	}
	recs, err := h.st.Partitions(ctx)
	if err != nil || len(recs) != 0 {
		t.Fatalf("a release recorded %+v (%v)", recs, err)
	}

	stuck = true
	if err := h.svc.release(ctx, "/var/mnt/cache", 0); err == nil {
		t.Fatal("a mount busy at every try was released")
	}
}
