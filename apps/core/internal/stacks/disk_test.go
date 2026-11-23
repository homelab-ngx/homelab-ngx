package stacks

import (
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestOwnedBytesCountsAHardLinkOnce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	data := make([]byte, 64<<10)
	if err := os.MkdirAll(filepath.Join(root, "var", "lib", "plex"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "var", "lib", "plex", "db")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(root, "var", "lib", "plex", "db.link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nix/store", filepath.Join(root, "store")); err != nil {
		t.Fatal(err)
	}

	byUID, base, err := ownedBytes(root)
	if err != nil {
		t.Fatal(err)
	}
	me := uint32(os.Getuid())
	if base != me {
		t.Errorf("base = %d, want the root's owner %d", base, me)
	}
	var want int64
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && filepath.Base(p) != "db.link" {
			want += fi.Sys().(*syscall.Stat_t).Blocks * 512
		}
		return nil
	})
	if got := byUID[me]; got != want || got < int64(len(data)) {
		t.Errorf("bytes = %d, want %d (the link counted once, the symlink not followed)", got, want)
	}
}

func TestUnitDiskGoesByTheUnitsUser(t *testing.T) {
	t.Parallel()
	passwd := []byte("root:x:0:0::/root:/bin/sh\nplex:x:193:193::/var/lib/plex:/bin/false\nsonarr:x:274:274::/var/lib/sonarr:/bin/false\n")
	const base = 1000
	byUID := map[uint32]int64{base: 900, base + 193: 5000, base + 274: 0}
	got := unitDisk(byUID, base, passwd, map[string]string{
		"plex.service":       "plex",
		"sonarr.service":     "sonarr",
		"numeric.service":    "193",
		"watchstate.service": "root",
		"ghost.service":      "nobody-known",
	})
	want := map[string]int64{"plex.service": 5000, "sonarr.service": 0, "numeric.service": 5000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unitDisk = %v, want %v", got, want)
	}
}

func TestDiskRecordSurvivesARestart(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "stack-disk.json")
	a := &Service{host: Host{DiskFile: file}}
	a.diskMu.Lock()
	a.saveDiskLocked(map[string]diskRecord{"players": {Units: map[string]int64{"plex.service": 42}}})
	a.diskMu.Unlock()

	b := &Service{host: Host{DiskFile: file}}
	if n := b.diskOf("players", "plex.service"); n == nil || *n != 42 {
		t.Errorf("diskOf after a restart = %v, want 42", n)
	}
	if n := b.diskOf("players", "podman-watchstate.service"); n != nil {
		t.Errorf("a unit with no figure = %d, want none", *n)
	}
	b.forgetDisk("players")
	if n := (&Service{host: Host{DiskFile: file}}).diskOf("players", "plex.service"); n != nil {
		t.Errorf("a removed stack keeps %d", *n)
	}
}
