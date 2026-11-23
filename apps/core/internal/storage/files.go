package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

// The file browser can also read, write and delete what is in a browse
// root. Every change goes through an os.Root opened at the root, so neither
// a symlink nor a rename made between the check and the change leads out of
// it.

// maxEdit is the largest file the editor reads or writes.
const maxEdit = 1 << 20

var (
	// ErrNoSuchFile means the path is in a browse root but nothing is there.
	ErrNoSuchFile = errors.New("no such file or folder")
	// ErrNotAFile means the path is a folder or a device, not a file.
	ErrNotAFile = errors.New("not a regular file")
	// ErrSymlink means the path is a symlink, which is not written through.
	ErrSymlink = errors.New("the path is a symlink")
	// ErrChangedOnDisk means the file changed since the editor read it.
	ErrChangedOnDisk = errors.New("the file changed on disk since it was read")
	// ErrExists means a new file's path is taken.
	ErrExists = errors.New("a file or folder with that name exists")
	// ErrIsRoot means the path is a browse root, which is not deleted here.
	ErrIsRoot = errors.New("a subvolume, mounted partition or stack folder is not deleted here")
	// ErrHoldsRoot means the folder holds a browse root.
	ErrHoldsRoot = errors.New("the folder holds a subvolume, mounted partition or stack folder")
	// ErrMountPoint means something is mounted in the folder.
	ErrMountPoint = errors.New("something is mounted in the folder")
	// ErrTooLarge means the content is over maxEdit.
	ErrTooLarge = errors.New("the file is larger than 1 MiB")
)

// place resolves the folder of path and returns the browse root it is in,
// the path relative to that root, and the path with its folder resolved. The
// last name is not resolved, so a symlink there is the link itself. root is
// "" when the folder is above every root; err is then ErrOutsideRoots.
func (s *Service) place(ctx context.Context, path string) (root, rel, full string, roots []string, err error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean == "/" {
		return "", "", "", nil, ErrOutsideRoots
	}
	dir, root, roots, err := s.locate(ctx, filepath.Dir(clean))
	if errors.Is(err, ErrNoSuchFolder) {
		return "", "", "", nil, ErrNoSuchFile
	} else if err != nil {
		return "", "", "", nil, err
	}
	full = filepath.Join(dir, filepath.Base(clean))
	if root == "" {
		return "", "", full, roots, ErrOutsideRoots
	}
	if rel, err = filepath.Rel(root, full); err != nil {
		return "", "", "", nil, ErrOutsideRoots
	}
	return root, rel, full, roots, nil
}

// version names one state of a file: its modification time and size.
func version(info fs.FileInfo) string {
	return strconv.FormatInt(info.ModTime().UnixNano(), 10) + "-" + strconv.FormatInt(info.Size(), 10)
}

func hostFile(path, root string, info fs.FileInfo) HostFile {
	return HostFile{Path: path, Root: root, Size: info.Size(), ModifiedAt: info.ModTime(), Version: version(info)}
}

// ReadFile reads a file in a browse root. A file over 1 MiB, or one that is
// not UTF-8, comes back as binary, without its content.
func (s *Service) ReadFile(ctx context.Context, path string) (HostFile, error) {
	root, rel, _, _, err := s.place(ctx, path)
	if err != nil {
		return HostFile{}, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return HostFile{}, err
	}
	defer r.Close()
	f, err := r.Open(rel) // follows a symlink, as long as it stays in the root
	if err != nil {
		return HostFile{}, fileErr(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return HostFile{}, err
	}
	if !info.Mode().IsRegular() {
		return HostFile{}, ErrNotAFile
	}
	out := hostFile(filepath.Join(root, rel), root, info)
	if info.Size() > maxEdit {
		out.Binary = true
		return out, nil
	}
	b, err := io.ReadAll(io.LimitReader(f, maxEdit+1))
	if err != nil {
		return HostFile{}, err
	}
	if len(b) > maxEdit || !utf8.Valid(b) {
		out.Binary = true
		return out, nil
	}
	content := string(b)
	out.Content = &content
	return out, nil
}

// WriteFile writes a file in a browse root. It writes a temporary file next
// to it and renames that over it, so a reader sees the old file or the new
// one. An existing file keeps its mode and owner; a new one takes its
// folder's owner and mode 0644.
func (s *Service) WriteFile(ctx context.Context, in FileWrite) (HostFile, error) {
	if len(in.Content) > maxEdit {
		return HostFile{}, ErrTooLarge
	}
	root, rel, _, _, err := s.place(ctx, in.Path)
	if err != nil {
		return HostFile{}, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return HostFile{}, err
	}
	defer r.Close()

	mode := fs.FileMode(0o644)
	var uid, gid int
	switch info, err := r.Lstat(rel); {
	case err == nil:
		switch {
		case in.Create:
			return HostFile{}, ErrExists
		case info.Mode()&fs.ModeSymlink != 0:
			return HostFile{}, ErrSymlink
		case !info.Mode().IsRegular():
			return HostFile{}, ErrNotAFile
		case in.Version != "" && in.Version != version(info):
			return HostFile{}, ErrChangedOnDisk
		}
		mode = info.Mode().Perm()
		uid, gid = owner(info)
	case errors.Is(err, fs.ErrNotExist):
		if in.Version != "" {
			return HostFile{}, ErrChangedOnDisk // deleted since it was read
		}
		dir, err := r.Stat(filepath.Dir(rel))
		if err != nil {
			return HostFile{}, fileErr(err)
		}
		uid, gid = owner(dir)
	default:
		return HostFile{}, fileErr(err)
	}

	tmp, err := tempName(rel)
	if err != nil {
		return HostFile{}, err
	}
	if err := writeTemp(r, tmp, in.Content, mode, uid, gid); err != nil {
		_ = r.Remove(tmp)
		return HostFile{}, err
	}
	if err := r.Rename(tmp, rel); err != nil {
		_ = r.Remove(tmp)
		return HostFile{}, err
	}
	info, err := r.Lstat(rel)
	if err != nil {
		return HostFile{}, err
	}
	s.forgetTree(root)
	return hostFile(filepath.Join(root, rel), root, info), nil
}

func writeTemp(r *os.Root, name, content string, mode fs.FileMode, uid, gid int) error {
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.WriteString(content)
	err = errors.Join(err, f.Chmod(mode)) // the umask may have cut the mode
	if os.Geteuid() == 0 || uid == os.Geteuid() {
		err = errors.Join(err, f.Chown(uid, gid))
	}
	err = errors.Join(err, f.Sync())
	return errors.Join(err, f.Close())
}

// tempName is a name next to rel that nothing else uses.
func tempName(rel string) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+".homelab-"+hex.EncodeToString(b)), nil
}

func owner(info fs.FileInfo) (uid, gid int) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid)
	}
	return os.Geteuid(), os.Getegid()
}

// DeleteEntry deletes a file, a symlink (never what it points to) or a
// folder with everything in it. A folder that holds anything needs confirm
// to be its name. A browse root, a folder that holds one and a folder that
// something is mounted in are refused.
func (s *Service) DeleteEntry(ctx context.Context, path, confirm string) error {
	root, rel, full, roots, err := s.place(ctx, path)
	if slices.Contains(roots, full) {
		return ErrIsRoot
	}
	if err != nil {
		return err
	}
	if slices.ContainsFunc(roots, func(r string) bool { return inside(full, r) }) {
		return ErrHoldsRoot
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	info, err := r.Lstat(rel)
	if err != nil {
		return fileErr(err)
	}
	if !info.IsDir() {
		if err := r.Remove(rel); err != nil {
			return fileErr(err)
		}
		s.forgetTree(root)
		return nil
	}
	empty, err := emptyDir(r, rel)
	if err != nil {
		return err
	}
	if !empty && confirm != filepath.Base(rel) {
		return fmt.Errorf("%w: type the folder's name to delete it and everything in it", ErrConfirm)
	}
	if err := sameDevice(r, rel, info); err != nil {
		return err
	}
	if err := r.RemoveAll(rel); err != nil {
		return err
	}
	s.forgetTree(root)
	return nil
}

func emptyDir(r *os.Root, rel string) (bool, error) {
	f, err := r.Open(rel)
	if err != nil {
		return false, fileErr(err)
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return len(names) == 0, err
}

// sameDevice refuses a folder with a mount point in it: RemoveAll would
// delete what is mounted there.
func sameDevice(r *os.Root, rel string, top fs.FileInfo) error {
	st, ok := top.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return fs.WalkDir(r.FS(), rel, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if s, ok := info.Sys().(*syscall.Stat_t); ok && s.Dev != st.Dev {
			return ErrMountPoint
		}
		return nil
	})
}

// forgetTree drops root's kept bcdu run: its figures no longer hold.
func (s *Service) forgetTree(root string) {
	s.dataMu.Lock()
	delete(s.trees, root)
	s.dataMu.Unlock()
}

// fileErr maps a missing path to ErrNoSuchFile and a path that leaves the
// root to ErrOutsideRoots.
func fileErr(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrNoSuchFile
	case errors.Is(err, syscall.ENOTDIR):
		return ErrNoSuchFile
	case strings.Contains(err.Error(), "path escapes from parent"):
		return ErrOutsideRoots
	}
	return err
}
