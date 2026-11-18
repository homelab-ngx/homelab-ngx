package storage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"homelab-ngx.com/core/internal/storage/bcachefs"
)

// The file browser is read-only and confined to the browse roots: the
// subvolumes and the host folders bound into stacks. Above a root only the
// folders that lead to one are listed, so the operator can walk down from /
// without seeing the rest of the host.

// browseLimit is the most entries one listing returns. A larger folder is cut
// by name and says so.
const browseLimit = 5000

// treeTTL is how long one bcdu run of a browse root serves its folders.
const treeTTL = 30 * time.Minute

// treeTimeout bounds a bcdu run, which outlives the request that started it.
const treeTimeout = 2 * time.Minute

// ErrOutsideRoots means the path is neither in a browse root nor on the way
// to one.
var ErrOutsideRoots = errors.New("the path is outside the folders that can be browsed")

// ErrNoSuchFolder means the path is in a browse root but is not a folder.
var ErrNoSuchFolder = errors.New("no such folder")

// FolderSource lists host folders that are browse roots besides the
// subvolumes; *stacks.Service satisfies it.
type FolderSource interface {
	HostFolders(ctx context.Context) ([]string, error)
}

// UseStackFolders makes the stacks' host folders browse roots.
func (s *Service) UseStackFolders(f FolderSource) { s.folders = f }

type folderTree struct {
	at      time.Time
	folders map[string]bcachefs.FolderUsage // by full path
}

// browseRoots are the folders that can be browsed, symlinks resolved. One
// that does not exist is left out.
func (s *Service) browseRoots(ctx context.Context) []string {
	var paths []string
	if st, err := s.Status(ctx); err != nil {
		s.log.Warn("subvolumes not readable for the file browser", "error", err)
	} else {
		for _, sv := range st.Subvolumes {
			paths = append(paths, sv.Path)
		}
	}
	if s.folders != nil {
		fs, err := s.folders.HostFolders(ctx)
		if err != nil {
			s.log.Warn("stack folders not readable for the file browser", "error", err)
		}
		paths = append(paths, fs...)
	}
	var out []string
	for _, p := range paths {
		if r, err := filepath.EvalSymlinks(p); err == nil && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// inside reports whether p is dir or below it.
func inside(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// locate resolves path and places it: root is the outermost browse root it
// is in, or "" when it is above one. Anything else is ErrOutsideRoots.
func (s *Service) locate(ctx context.Context, path string) (real, root string, roots []string, err error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", "", nil, ErrOutsideRoots
	}
	roots = s.browseRoots(ctx)
	real, err = filepath.EvalSymlinks(clean)
	if err != nil {
		// Only a path that would be in a root may learn that it is missing.
		if slices.ContainsFunc(roots, func(r string) bool { return inside(r, clean) }) {
			return "", "", nil, ErrNoSuchFolder
		}
		return "", "", nil, ErrOutsideRoots
	}
	above := false
	for _, r := range roots {
		if inside(r, real) && (root == "" || len(r) < len(root)) {
			root = r
		}
		above = above || inside(real, r)
	}
	if root == "" && !above {
		return "", "", nil, ErrOutsideRoots
	}
	return real, root, roots, nil
}

// Browse lists the folder at path. In a browse root that is everything in
// it; above one, only the folders that lead to a root.
func (s *Service) Browse(ctx context.Context, path string) (Listing, error) {
	real, root, roots, err := s.locate(ctx, path)
	if err != nil {
		return Listing{}, err
	}
	out := Listing{Path: real, Root: root, Entries: []Entry{}}
	var names []string
	if root == "" {
		for _, r := range roots {
			if !inside(real, r) {
				continue
			}
			next, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(r, real), "/"), "/")
			if !slices.Contains(names, next) {
				names = append(names, next)
			}
		}
		slices.Sort(names)
	} else {
		des, err := os.ReadDir(real)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				return Listing{}, ErrNoSuchFolder
			}
			return Listing{}, err
		}
		for _, d := range des {
			names = append(names, d.Name())
		}
	}
	out.Total = len(names)
	if len(names) > browseLimit {
		names, out.Truncated = names[:browseLimit], true
	}
	owners := map[string]string{}
	for _, name := range names {
		info, err := os.Lstat(filepath.Join(real, name))
		if err != nil {
			continue // gone since the directory was read
		}
		out.Entries = append(out.Entries, entryOf(info, owners))
	}
	return out, nil
}

// entryOf describes one directory entry from its lstat. names caches the
// user and group lookups of one listing.
func entryOf(info fs.FileInfo, names map[string]string) Entry {
	e := Entry{Name: info.Name(), Type: "other", Size: info.Size(), ModifiedAt: info.ModTime(), Mode: info.Mode().String()}
	switch m := info.Mode(); {
	case m.IsDir():
		e.Type = "dir"
	case m.IsRegular():
		e.Type = "file"
	case m&fs.ModeSymlink != 0:
		e.Type = "symlink"
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		e.DiskBytes = st.Blocks * 512
		e.Owner = idName(names, "u", st.Uid)
		e.Group = idName(names, "g", st.Gid)
	}
	return e
}

// idName is the name of a user ("u") or group ("g") id, or the number when
// it has none.
func idName(names map[string]string, kind string, id uint32) string {
	n := strconv.FormatUint(uint64(id), 10)
	if v, ok := names[kind+n]; ok {
		return v
	}
	name := n
	if kind == "u" {
		if u, err := user.LookupId(n); err == nil {
			name = u.Username
		}
	} else if g, err := user.LookupGroupId(n); err == nil {
		name = g.Name
	}
	names[kind+n] = name
	return name
}

// BrowseUsage returns bcdu's figures for the folders directly in path. They
// come from one bcdu run of the path's browse root, kept for treeTTL, so the
// first call for a root takes a few seconds and the rest are lookups. Not
// available above a root, off bcachefs, or when bcdu fails.
func (s *Service) BrowseUsage(ctx context.Context, path string) (FolderFigures, error) {
	real, root, _, err := s.locate(ctx, path)
	if err != nil {
		return FolderFigures{}, err
	}
	out := FolderFigures{Folders: map[string]bcachefs.FolderUsage{}}
	if root == "" {
		return out, nil
	}
	if !s.onBcachefs(root) {
		out.Reason = "not_bcachefs"
		return out, nil
	}
	t, ok := s.tree(root)
	if !ok {
		s.measureMu.Lock()
		defer s.measureMu.Unlock()
		if t, ok = s.tree(root); !ok { // another request may have measured it meanwhile
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), treeTimeout)
			defer cancel()
			folders, err := s.bc.FolderTree(ctx, root)
			s.shrinkBtreeCache()
			if err != nil {
				s.log.Error("failed to measure folders for the file browser", "root", root, "error", err)
				out.Reason = "failed"
				return out, nil
			}
			t = folderTree{at: s.now(), folders: folders}
			s.dataMu.Lock()
			s.trees[root] = t
			s.dataMu.Unlock()
		}
	}
	out.Available, out.MeasuredAt = true, &t.at
	for p, u := range t.folders {
		if filepath.Dir(p) == real && p != real {
			out.Folders[filepath.Base(p)] = u
		}
	}
	return out, nil
}

// tree returns root's kept bcdu run, dropping every run older than treeTTL.
func (s *Service) tree(root string) (folderTree, bool) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	for r, t := range s.trees {
		if s.now().Sub(t.at) >= treeTTL {
			delete(s.trees, r)
		}
	}
	t, ok := s.trees[root]
	return t, ok
}
