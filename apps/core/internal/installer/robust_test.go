package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
)

// changingDisks lists what each call's turn says, then the last for ever,
// and records the partitions it was asked to delete.
type changingDisks struct {
	lists   [][]InstallDisk
	calls   int
	deleted []string
}

func (c *changingDisks) List(context.Context) ([]InstallDisk, error) {
	i := min(c.calls, len(c.lists)-1)
	c.calls++
	return c.lists[i], nil
}

func (c *changingDisks) DeletePartition(_ context.Context, path string) error {
	c.deleted = append(c.deleted, path)
	return nil
}

// A closed tab no longer holds the installer until the daemon restarts: the
// hold lapses when its page is silent, and the console's code takes it over.
func TestClaimLapsesAndTakeOver(t *testing.T) {
	s := New(cmdrunnertest.NewFake(), fakeDisks{list: []InstallDisk{assess(windows(512 * mib))}}, quiet())
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	ctx := context.Background()
	first, err := s.Claim()
	if err != nil {
		t.Fatal(err)
	}
	// The page keeps asking: the hold stays.
	now = now.Add(claimIdle - time.Second)
	s.Seen(first)
	now = now.Add(claimIdle - time.Second)
	if _, err := s.Claim(); !errors.Is(err, ErrClaimed) {
		t.Fatalf("a claim while the holder's page is open: %v", err)
	}
	// A stranger's token does not count as the holder.
	s.Seen("someone else")
	now = now.Add(2 * time.Second)
	if st, _ := s.State(ctx); st.Claimed {
		t.Fatal("still claimed after ten silent minutes")
	}
	second, err := s.Claim()
	if err != nil || second == first {
		t.Fatalf("a claim after the hold lapsed: %q %v", second, err)
	}
	if err := s.PowerOff(ctx, first); !errors.Is(err, ErrClaimed) {
		t.Fatalf("the old token still works: %v", err)
	}

	// The console's code takes it over at once; wrong codes lock it.
	dir := t.TempDir()
	if err := s.WriteCode(filepath.Join(dir, "run", "code")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "run", "code"))
	code := strings.TrimSpace(string(b))
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		t.Fatalf("the code is %q, not six digits", code)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	if _, err := s.TakeOver(wrong); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a wrong code: %v", err)
	}
	third, err := s.TakeOver(" " + code + "\n")
	if err != nil || third == second {
		t.Fatalf("the right code: %q %v", third, err)
	}
	if err := s.PowerOff(ctx, second); !errors.Is(err, ErrClaimed) {
		t.Fatalf("the token before the take-over still works: %v", err)
	}
	for range takeOverTries {
		_, _ = s.TakeOver(wrong)
	}
	if _, err := s.TakeOver(code); !errors.Is(err, ErrLocked) {
		t.Fatalf("after %d wrong codes: %v", takeOverTries, err)
	}
	now = now.Add(takeOverLock + time.Second)
	if _, err := s.TakeOver(code); err != nil {
		t.Fatalf("after the lock-out: %v", err)
	}
	// An install that runs is not given away to a new claim.
	s.step = StepInstalling
	now = now.Add(time.Hour)
	if _, err := s.Claim(); !errors.Is(err, ErrClaimed) {
		t.Fatalf("a claim during an install: %v", err)
	}
}

// A folder of zones, or a file that is no zone, is not a time zone.
func TestTimezoneIsAZoneFile(t *testing.T) {
	old := zoneinfo
	zoneinfo = t.TempDir()
	defer func() { zoneinfo = old }()
	writeFiles(t, zoneinfo, map[string]string{"Europe/Stockholm": "TZif2 and the rest", "tzdata.zi": "# version 2026a\n"})
	s := New(cmdrunnertest.NewFake(), fakeDisks{list: []InstallDisk{assess(windows(512 * mib))}}, quiet())
	tok, _ := s.Claim()
	for _, tz := range []string{"Europe", "tzdata.zi", "../etc/passwd", "Europe/Oslo"} {
		if err := s.Install(context.Background(), tok, "nvme0n1", false, Setup{Hostname: "box", Timezone: tz}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "time zone") {
			t.Errorf("time zone %q: %v", tz, err)
		}
	}
}

// What a failed install made in the free space is removed, and nothing
// that was there before it.
func TestFailedInstallRemovesItsPartitions(t *testing.T) {
	before := assess(windows(512 * mib))
	after := before
	after.Partitions = append(slices.Clone(before.Partitions),
		part(5, "nvme0n1p5", 153*gib, 2*gib, typeRootX64, ""), part(6, "nvme0n1p6", 155*gib, 32*gib, typeVar, "ext4"))
	disks := &changingDisks{lists: [][]InstallDisk{{after}}}
	f := cmdrunnertest.NewFake()
	f.On("sfdisk", "--dump").Reply("label: gpt\n")
	f.On("udevadm").Reply("")
	s := New(f, disks, quiet())
	s.removeNew(context.Background(), before)
	if !slices.Equal(disks.deleted, []string{"/dev/nvme0n1p5", "/dev/nvme0n1p6"}) {
		t.Fatalf("deleted %v", disks.deleted)
	}
}

// Temporary partitions left by an install that lost power are deleted when
// the installer starts; the stick's own disk is left alone.
func TestSweepRemovesReservations(t *testing.T) {
	left := assess(windows(512 * mib))
	held := part(5, "nvme0n1p5", 153*gib, 2*gib, reservedType, "")
	held.Label = reservedName
	left.Partitions = append(left.Partitions, held)
	stick := InstallDisk{Name: "sdb", Path: "/dev/sdb", InUse: true, Partitions: []InstallPartition{held}}
	f := cmdrunnertest.NewFake()
	f.On("sfdisk", "--dump", "/dev/nvme0n1").Reply("label: gpt\n/dev/nvme0n1p4 : start=1, size=2, type=X\n/dev/nvme0n1p5 : start=3, size=4, type=" + reservedType + `, name="` + reservedName + "\"\n")
	f.On("sfdisk", "-q", "--delete").Reply("")
	s := New(f, fakeDisks{list: []InstallDisk{left, stick, assess(windows(512 * mib))}}, quiet())
	s.Sweep(context.Background())
	if got := argv(f, "sfdisk", "-q", "--delete"); got != "sfdisk -q --delete /dev/nvme0n1 5" {
		t.Fatalf("deleted:\n%s\nall sfdisk calls:\n%s", got, argv(f, "sfdisk"))
	}
}

// Two plans, or a plan during an install, each get definitions of their own.
func TestDefinitionsArePerCall(t *testing.T) {
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"00-esp.conf": "[Partition]\nType=esp\n", "10-root.conf": "[Partition]\nType=root\n"})
	old, oldMount := definitions, mountDir
	definitions, mountDir = src, filepath.Join(t.TempDir(), "esp")
	defer func() { definitions, mountDir = old, oldMount }()
	a, doneA, err := definitionsFor(true)
	if err != nil {
		t.Fatal(err)
	}
	b, doneB, err := definitionsFor(true)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("both calls share %s", a)
	}
	doneB()
	if _, err := os.Stat(filepath.Join(a, "10-root.conf")); err != nil {
		t.Fatalf("one call's end removed the other's definitions: %v", err)
	}
	doneA()
	if _, err := os.Stat(a); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the definitions were left behind")
	}
}

// UDisks may list the disk before it has read the new partition table.
func TestNewPartitionsWaitForUDisks(t *testing.T) {
	oldTries, oldWait := settleTries, settleWait
	settleTries, settleWait = 3, time.Millisecond
	defer func() { settleTries, settleWait = oldTries, oldWait }()
	before := assess(windows(512 * mib))
	after := before
	after.Partitions = append(slices.Clone(before.Partitions), part(6, "nvme0n1p6", 155*gib, 32*gib, typeVar, "ext4"))
	disks := &changingDisks{lists: [][]InstallDisk{{InstallDisk{Name: "nvme0n1"}}, {before}, {after}}}
	s := New(cmdrunnertest.NewFake(), disks, quiet())
	esp, v, err := s.newPartitions(context.Background(), "nvme0n1", "")
	if err != nil || esp != "/dev/nvme0n1p1" || v != "/dev/nvme0n1p6" {
		t.Fatalf("esp %q var %q: %v (after %d listings)", esp, v, err, disks.calls)
	}
	never := &changingDisks{lists: [][]InstallDisk{{before}}}
	s = New(cmdrunnertest.NewFake(), never, quiet())
	if _, _, err := s.newPartitions(context.Background(), "nvme0n1", ""); err == nil || never.calls != 3 {
		t.Fatalf("a table that never shows: %v after %d listings", err, never.calls)
	}
}

func TestMachine(t *testing.T) {
	oldMem, oldVars := meminfo, efivars
	dir := t.TempDir()
	meminfo, efivars = filepath.Join(dir, "meminfo"), filepath.Join(dir, "efivars")
	defer func() { meminfo, efivars = oldMem, oldVars }()
	if m := machine(); m.Memory != 0 || m.SecureBoot || m.BootEntries {
		t.Fatalf("nothing to read: %+v", m)
	}
	writeFiles(t, dir, map[string]string{
		"meminfo": "MemTotal:       16303412 kB\nMemFree:  1 kB\n",
		"efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b2c": "\x06\x00\x00\x00\x01",
	})
	if m := machine(); m.Memory != 16303412<<10 || !m.SecureBoot || !m.BootEntries {
		t.Fatalf("machine = %+v", m)
	}
}
