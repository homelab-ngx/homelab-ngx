package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestFileReadAndSave(t *testing.T) {
	t.Parallel()
	h, tmp := browseHarness(t)
	ctx := context.Background()
	file := filepath.Join(tmp, "pool/media/a.txt")
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := h.svc.ReadFile(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if f.Content == nil || *f.Content != "hello" || f.Binary || f.Size != 5 || f.Version == "" || f.Root != filepath.Join(tmp, "pool/media") {
		t.Fatalf("read = %+v", f)
	}

	saved, err := h.svc.WriteFile(ctx, FileWrite{Path: file, Content: "hello, world", Version: f.Version})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Size != 12 || saved.Version == f.Version || saved.Content != nil {
		t.Fatalf("saved = %+v", saved)
	}
	if b, _ := os.ReadFile(file); string(b) != "hello, world" {
		t.Fatalf("on disk: %q", b)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want the file's own 0600", info.Mode().Perm())
	}
	if des, _ := os.ReadDir(filepath.Dir(file)); len(des) != 3 {
		t.Errorf("a temporary file was left: %d entries", len(des))
	}

	// The first read's version is stale now.
	if _, err := h.svc.WriteFile(ctx, FileWrite{Path: file, Content: "x", Version: f.Version}); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("stale version: %v, want ErrChangedOnDisk", err)
	}
	// Without a version it saves regardless.
	if _, err := h.svc.WriteFile(ctx, FileWrite{Path: file, Content: "x"}); err != nil {
		t.Fatalf("no version: %v", err)
	}
}

func TestFileCreate(t *testing.T) {
	t.Parallel()
	h, tmp := browseHarness(t)
	ctx := context.Background()
	media := filepath.Join(tmp, "pool/media")

	if _, err := h.svc.WriteFile(ctx, FileWrite{Path: filepath.Join(media, "a.txt"), Create: true}); !errors.Is(err, ErrExists) {
		t.Fatalf("create over a file: %v, want ErrExists", err)
	}
	if _, err := h.svc.WriteFile(ctx, FileWrite{Path: filepath.Join(media, "films"), Content: "x"}); !errors.Is(err, ErrNotAFile) {
		t.Fatalf("save over a folder: %v, want ErrNotAFile", err)
	}
	if _, err := h.svc.WriteFile(ctx, FileWrite{Path: filepath.Join(media, "nope/new.txt"), Create: true}); !errors.Is(err, ErrNoSuchFile) {
		t.Fatalf("create in a missing folder: %v, want ErrNoSuchFile", err)
	}
	if _, err := h.svc.WriteFile(ctx, FileWrite{Path: filepath.Join(media, "gone.txt"), Content: "x", Version: "1-1"}); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("save a deleted file: %v, want ErrChangedOnDisk", err)
	}

	dir := filepath.Join(media, "films")
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	f, err := h.svc.WriteFile(ctx, FileWrite{Path: filepath.Join(dir, "new.conf"), Content: "a = 1\n", Create: true})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("new file's mode = %v, want 0644", info.Mode().Perm())
	}
	want, _ := os.Stat(dir)
	if a, b := info.Sys().(*syscall.Stat_t), want.Sys().(*syscall.Stat_t); a.Uid != b.Uid || a.Gid != b.Gid {
		t.Errorf("new file's owner = %d:%d, want its folder's %d:%d", a.Uid, a.Gid, b.Uid, b.Gid)
	}
}

func TestFileBinary(t *testing.T) {
	t.Parallel()
	h, tmp := browseHarness(t)
	ctx := context.Background()
	media := filepath.Join(tmp, "pool/media")
	if err := os.WriteFile(filepath.Join(media, "bin"), []byte{0xff, 0xfe, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "big"), []byte(strings.Repeat("a", maxEdit+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bin", "big"} {
		f, err := h.svc.ReadFile(ctx, filepath.Join(media, name))
		if err != nil {
			t.Fatal(err)
		}
		if !f.Binary || f.Content != nil {
			t.Errorf("%s: %+v, want binary without content", name, f)
		}
	}
	if _, err := h.svc.ReadFile(ctx, filepath.Join(media, "films")); !errors.Is(err, ErrNotAFile) {
		t.Errorf("read a folder: %v, want ErrNotAFile", err)
	}
	if _, err := h.svc.ReadFile(ctx, filepath.Join(media, "nope")); !errors.Is(err, ErrNoSuchFile) {
		t.Errorf("read a missing file: %v, want ErrNoSuchFile", err)
	}
	if _, err := h.svc.WriteFile(ctx, FileWrite{Path: filepath.Join(media, "big"), Content: strings.Repeat("a", maxEdit+1)}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("save too much: %v, want ErrTooLarge", err)
	}
}

func TestFileStaysInTheRoots(t *testing.T) {
	t.Parallel()
	h, tmp := browseHarness(t)
	ctx := context.Background()
	media := filepath.Join(tmp, "pool/media")
	secret := filepath.Join(tmp, "secret/s.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmp, "secret"), filepath.Join(media, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(media, "link")); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{secret, filepath.Join(media, "out/s.txt"), media + "/../private/x", filepath.Join(media, "link"), "pool/media/a.txt", "", "/"} {
		if _, err := h.svc.ReadFile(ctx, p); !errors.Is(err, ErrOutsideRoots) {
			t.Errorf("ReadFile(%q) = %v, want ErrOutsideRoots", p, err)
		}
		if _, err := h.svc.WriteFile(ctx, FileWrite{Path: p, Content: "x", Create: true}); !errors.Is(err, ErrOutsideRoots) && !errors.Is(err, ErrExists) {
			t.Errorf("WriteFile(%q) = %v, want ErrOutsideRoots", p, err)
		}
	}
	if _, err := h.svc.WriteFile(ctx, FileWrite{Path: filepath.Join(media, "link"), Content: "x"}); !errors.Is(err, ErrSymlink) {
		t.Errorf("save through a symlink: %v, want ErrSymlink", err)
	}
	// Deleting the link leaves what it points to.
	if err := h.svc.DeleteEntry(ctx, filepath.Join(media, "link"), ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(secret); string(b) != "secret" {
		t.Fatalf("the symlink's target changed: %q", b)
	}
}

func TestDeleteEntry(t *testing.T) {
	t.Parallel()
	h, tmp := browseHarness(t)
	ctx := context.Background()
	media := filepath.Join(tmp, "pool/media")
	// A stack's folder inside the media root.
	h.svc.UseStackFolders(folderList{media, filepath.Join(tmp, "data"), filepath.Join(media, "films/old")})

	for p, want := range map[string]error{
		media:                             ErrIsRoot,
		filepath.Join(tmp, "data"):        ErrIsRoot,
		filepath.Join(media, "films/old"): ErrIsRoot,
		filepath.Join(media, "films"):     ErrHoldsRoot,
		filepath.Join(tmp, "pool"):        ErrOutsideRoots,
		filepath.Join(tmp, "secret"):      ErrOutsideRoots,
		filepath.Join(media, "nope"):      ErrNoSuchFile,
	} {
		if err := h.svc.DeleteEntry(ctx, p, filepath.Base(p)); !errors.Is(err, want) {
			t.Errorf("DeleteEntry(%q) = %v, want %v", p, err, want)
		}
	}

	if err := h.svc.DeleteEntry(ctx, filepath.Join(media, "a.txt"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(media, "a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a.txt still there: %v", err)
	}

	empty := filepath.Join(media, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DeleteEntry(ctx, empty, ""); err != nil {
		t.Fatalf("an empty folder needs no name: %v", err)
	}

	music := filepath.Join(media, "music")
	if err := os.WriteFile(filepath.Join(music, "song"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, confirm := range []string{"", "films", music} {
		if err := h.svc.DeleteEntry(ctx, music, confirm); !errors.Is(err, ErrConfirm) {
			t.Errorf("confirm %q: %v, want ErrConfirm", confirm, err)
		}
	}
	if err := h.svc.DeleteEntry(ctx, music, "music"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(music); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("music still there: %v", err)
	}
}

// A stack's folder belongs to the stack's own user: a saved file keeps that
// owner, and a new one takes it from its folder. Only root can set them.
func TestFileOwnerAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to own files as another user")
	}
	t.Parallel()
	h, tmp := browseHarness(t)
	ctx := context.Background()
	dir := filepath.Join(tmp, "data")
	old := filepath.Join(dir, "old.conf")
	if err := os.WriteFile(old, []byte("a"), 0o640); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, old} {
		if err := os.Chown(p, 2000, 2001); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.svc.WriteFile(ctx, FileWrite{Path: old, Content: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.WriteFile(ctx, FileWrite{Path: filepath.Join(dir, "new.conf"), Create: true}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old.conf", "new.conf"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if st := info.Sys().(*syscall.Stat_t); st.Uid != 2000 || st.Gid != 2001 {
			t.Errorf("%s owner = %d:%d, want 2000:2001", name, st.Uid, st.Gid)
		}
	}
}
