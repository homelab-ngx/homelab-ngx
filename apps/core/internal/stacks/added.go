package stacks

import (
	"context"
	"fmt"
	"sort"
	"time"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/storage"
	"homelab-ngx.com/core/internal/store"
)

// An added folder is a host folder the operator binds into a stack on top of
// what its manifest asks for: a library a media stack only reads, a share
// another stack writes. The stack's repo is not changed, so there is nothing
// to approve, as with a path variable's value. The folders are stored with
// the stack's variables and bound after the manifest's own binds; a folder
// whose mount the manifest comes to claim is left out until it is moved.

// maxFolders bounds a stack's added folders.
const maxFolders = 32

// foldersView is the stack's added folders as the API shows them, each
// marked with what in the running manifest shadows it.
func (s *Service) foldersView(ctx context.Context, r store.Stack, folders []storedFolder) []StackFolder {
	out := make([]StackFolder, 0, len(folders))
	m, err := s.runningManifest(ctx, r)
	if err != nil {
		m = Manifest{}
	}
	for _, f := range folders {
		v := StackFolder{
			Path: f.Host, Mount: f.Mount, ReadOnly: f.ReadOnly,
			ShadowedBy: shadowedBy(m, f.Mount), UpdatedAt: time.UnixMilli(f.UpdatedAt),
		}
		if uid, gid, err := s.statOwner(f.Host); err == nil {
			fu, fg := int64(uid), int64(gid)
			v.FolderUID, v.FolderGID = &fu, &fg
		}
		out = append(out, v)
	}
	return out
}

// SetFolders replaces the stack's added folders. Each host path follows the
// rules of a path variable's value; each mount must be one a manifest may
// use, and neither at, above nor under another bind's: the manifests'
// (approved, fetched and running) or another added folder's. A change takes
// effect at the stack's next restart.
func (s *Service) SetFolders(ctx context.Context, name string, in StackFoldersSet) (Stack, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return Stack{}, err
	}
	if len(in.Folders) > maxFolders {
		return Stack{}, httpx.Unprocessable("too_many_folders", "a stack takes at most %d added folders", maxFolders)
	}
	approved, fetched, err := manifests(r)
	if err != nil {
		return Stack{}, err
	}
	running, err := s.runningManifest(ctx, r)
	if err != nil {
		return Stack{}, err
	}
	now := s.now().UnixMilli()
	out := make([]storedFolder, 0, len(in.Folders))
	for _, f := range in.Folders {
		host := storage.Canonical(f.Path)
		if err := validHostPath(host); err != nil {
			return Stack{}, httpx.Unprocessable("invalid_path", "%v", err)
		}
		if err := s.statDir(host); err != nil {
			return Stack{}, httpx.Unprocessable("invalid_path", "%v", err)
		}
		if err := validMount(f.Mount); err != nil {
			return Stack{}, httpx.Unprocessable("invalid_mount", "%v", err)
		}
		for _, m := range []Manifest{running, approved, fetched} {
			if by := shadowedBy(m, f.Mount); by != "" {
				return Stack{}, httpx.Unprocessable("mount_taken", "%s overlaps %s; pick another mount", f.Mount, by)
			}
		}
		for _, o := range out {
			if mountsClash(f.Mount, o.Mount) {
				return Stack{}, httpx.Unprocessable("mount_taken", "%s overlaps the folder at %s; pick another mount", f.Mount, o.Mount)
			}
		}
		out = append(out, storedFolder{Host: host, Mount: f.Mount, ReadOnly: f.ReadOnly, UpdatedAt: now})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	err = func() error {
		s.varsMu.Lock()
		defer s.varsMu.Unlock()
		old, err := s.loadFolders(name)
		if err != nil {
			return err
		}
		for i, f := range out { // an unchanged folder keeps its time
			for _, was := range old {
				if was.Mount == f.Mount && was.Host == f.Host && was.ReadOnly == f.ReadOnly {
					out[i].UpdatedAt = was.UpdatedAt
				}
			}
		}
		if err := s.saveFolders(name, out); err != nil {
			return fmt.Errorf("storing the added folders: %w", err)
		}
		return nil
	}()
	if err != nil {
		return Stack{}, err
	}
	s.log.Info("stack folders set", "stack", name, "folders", len(out))
	return s.Get(ctx, name)
}
