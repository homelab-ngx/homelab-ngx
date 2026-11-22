package stacks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/store"
)

// A stack's manifest may recommend folders worth backing up and name a unit
// that prepares them (version 6). This file is what the backups package
// reads of that: where each recommended folder is on the host, the prepare
// unit and how to run it, and a call when a stack gains folders.

// BackupFolder is one folder a stack recommends backing up, resolved to the
// host directory that holds it.
type BackupFolder struct {
	Stack string
	// Running reports whether the stack's container is up; the prepare unit
	// can only run in a running stack.
	Running bool
	// Prepare is the stack's prepare unit, empty when it has none.
	Prepare string
	// Key names the folder within its stack: the declared path, or the name
	// of the path variable.
	Key   string
	Label string
	// HostPath is the directory on the host. Empty when Error is set.
	HostPath string
	Excludes []BackupExclude
	// Error says why the folder has no host path; the other folders are
	// still listed.
	Error string
}

// BackupExclude is a pattern a stack leaves out of a recommended folder.
type BackupExclude struct {
	Pattern string
	Reason  string
}

// prepareTimeout bounds a stack's prepare unit.
const prepareTimeout = 30 * time.Minute

// BackupFolders lists the folders every installed stack recommends, from the
// manifest of its current generation (the approved one while nothing is
// built). A folder that cannot be resolved carries an Error; the call fails
// only when the stacks cannot be read at all.
func (s *Service) BackupFolders(ctx context.Context) ([]BackupFolder, error) {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return nil, err
	}
	out := []BackupFolder{}
	for _, r := range rows {
		m, err := s.runningManifest(ctx, r)
		if err != nil {
			s.log.Warn("stack manifest not readable for its backup folders", "stack", r.Name, "error", err)
			continue
		}
		dirs := m.BackupDirs()
		if len(dirs) == 0 {
			continue
		}
		vars, folders, verr := s.loadBinds(r.Name)
		running := s.isRunning(ctx, r.Name)
		for _, d := range dirs {
			f := BackupFolder{
				Stack: r.Name, Running: running, Prepare: m.Backup.Prepare, Key: d.Key(), Label: d.Label,
				Excludes: make([]BackupExclude, 0, len(d.Exclude)),
			}
			for _, x := range d.Exclude {
				f.Excludes = append(f.Excludes, BackupExclude(x))
			}
			if verr != nil && (d.Variable != "" || len(m.pathVars()) > 0) {
				// Without the stored values a path's host directory is unknown.
				f.Error = fmt.Sprintf("the stack's variables cannot be read: %v", verr)
			} else if p, err := s.backupHostPath(r.Name, m, vars, folders, d); err != nil {
				f.Error = err.Error()
			} else {
				f.HostPath = p
			}
			out = append(out, f)
		}
	}
	return out, nil
}

// backupHostPath is where the recommended folder d of stack name is on the
// host: a variable's host directory, a path under a bind mapped through the
// bind, or a path in the stack's root. The result has no symlinks in the
// part that exists, and is inside the directory it was mapped into.
func (s *Service) backupHostPath(name string, m Manifest, vars []storedVar, folders []storedFolder, d BackupDir) (string, error) {
	if d.Variable != "" {
		v, ok := m.Variable(d.Variable)
		if !ok || v.Kind != VarPath {
			return "", fmt.Errorf("%s is not a path variable of the stack", d.Variable)
		}
		value, src := resolve(v, vars)
		if src == SourceUnset {
			return "", fmt.Errorf("the path %s has no value", d.Variable)
		}
		return within(value, value)
	}
	// The longest mount at or above the path is the bind that holds it.
	var bind *hostBind
	for _, b := range lenientBinds(m, vars, folders) {
		if _, ok := under(d.Path, []string{b.Mount}); ok && (bind == nil || len(b.Mount) > len(bind.Mount)) {
			bind = &b
		}
	}
	if bind != nil {
		return within(bind.Host, filepath.Join(bind.Host, strings.TrimPrefix(d.Path, bind.Mount)))
	}
	if v, ok := mountOf(m, d.Path); ok {
		return "", fmt.Errorf("the path %s that holds %s has no value", v.Name, d.Path)
	}
	root := filepath.Join(s.stateDir(name), "root")
	return within(root, filepath.Join(root, d.Path))
}

// lenientBinds are hostBinds without its refusal: a path variable with no
// value simply has no bind.
func lenientBinds(m Manifest, vars []storedVar, folders []storedFolder) []hostBind {
	var out []hostBind
	for _, st := range m.Storage {
		out = append(out, hostBind{st.HostPath, st.Mount, st.ReadOnly})
	}
	for _, d := range m.pathVars() {
		if value, src := resolve(d, vars); src != SourceUnset {
			out = append(out, hostBind{value, d.Mount, d.ReadOnly || narrowed(d, vars)})
		}
	}
	return withFolders(m, out, folders)
}

// mountOf is the path variable without a value whose mount holds p. Its
// directory inside the stack's root is not where the data would be.
func mountOf(m Manifest, p string) (Variable, bool) {
	for _, v := range m.pathVars() {
		if _, ok := under(p, []string{v.Mount}); ok {
			return v, true
		}
	}
	return Variable{}, false
}

// within resolves the symlinks of p, as far as p exists, and refuses a
// result outside root. A stack owns its root and its binds, so a link in
// them can point anywhere; an absolute one resolves against the host's root
// when followed from the host.
func within(root, p string) (string, error) {
	realRoot, err := resolveExisting(root)
	if err != nil {
		return "", err
	}
	real, err := resolveExisting(p)
	if err != nil {
		return "", err
	}
	if _, ok := under(real, []string{realRoot}); !ok {
		return "", fmt.Errorf("%s leads to %s, outside %s", p, real, root)
	}
	return real, nil
}

// resolveExisting is p with the symlinks of its deepest existing ancestor
// resolved; the part that does not exist yet is kept as written.
func resolveExisting(p string) (string, error) {
	rest := ""
	for cur := p; ; {
		_, err := os.Lstat(cur)
		if err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", fmt.Errorf("resolving %s: %w", cur, err)
			}
			return filepath.Join(real, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolving %s: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// RunPrepare starts unit inside the running stack and waits for it to
// finish. The unit is a oneshot, so start returns when its job is done and
// fails when the job did.
func (s *Service) RunPrepare(ctx context.Context, stack, unit string) error {
	if !ValidName(stack) || !unitRE.MatchString(unit) {
		return fmt.Errorf("prepare unit %q in stack %q: not a stack and a .service unit name", unit, stack)
	}
	if s.frozen(ctx, stack) {
		return fmt.Errorf("prepare unit %s in stack %s: the stack is frozen", unit, stack)
	}
	if _, err := s.run(ctx, prepareTimeout, "systemctl", "--machine="+stack, "start", "--", unit); err != nil {
		return fmt.Errorf("prepare unit %s in stack %s failed: %w", unit, stack, err)
	}
	s.log.Info("stack prepare unit ran", "stack", stack, "unit", unit)
	return nil
}

// OnBackupFoldersAdded registers fn, called once when an install or update
// of a stack ends healthy and its new generation recommends folders the
// generation before did not (all of them after an install). keys are those
// folders' keys. fn runs on the operation's goroutine.
func (s *Service) OnBackupFoldersAdded(fn func(ctx context.Context, stack string, keys []string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backupAdded = fn
}

// addedBackupKeys are the recommended folders of next that prev does not
// declare.
func addedBackupKeys(prev, next Manifest) []string {
	var keys []string
	for _, d := range next.BackupDirs() {
		if !slices.ContainsFunc(prev.BackupDirs(), func(o BackupDir) bool { return o.Key() == d.Key() }) {
			keys = append(keys, d.Key())
		}
	}
	return keys
}

// backupFoldersAdded tells the registered listener what generation g
// recommends beyond prev (nil after an install). A manifest that cannot be
// read tells nothing: the folders still show as not backed up.
func (s *Service) backupFoldersAdded(ctx context.Context, name string, prev *store.StackGeneration, g store.StackGeneration) {
	s.mu.Lock()
	fn := s.backupAdded
	s.mu.Unlock()
	if fn == nil {
		return
	}
	next, err := decode(g.Manifest)
	if err != nil {
		return
	}
	var before Manifest
	if prev != nil {
		if before, err = decode(prev.Manifest); err != nil {
			return
		}
	}
	if keys := addedBackupKeys(before, next); len(keys) > 0 {
		fn(ctx, name, keys)
	}
}

// FolderUsers lists, per installed stack, the host directories bound into
// it: storage grants and path variables with a value.
func (s *Service) FolderUsers(ctx context.Context) (map[string][]string, error) {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, r := range rows {
		m, err := s.runningManifest(ctx, r)
		if err != nil {
			continue
		}
		vars, folders, err := s.loadBinds(r.Name)
		if err != nil {
			continue
		}
		for _, b := range lenientBinds(m, vars, folders) {
			out[r.Name] = append(out[r.Name], b.Host)
		}
	}
	return out, nil
}

// HostFolders lists the host directories bound into the installed stacks:
// storage grants and path variables with a value. A stack whose manifest or
// variables cannot be read is skipped.
func (s *Service) HostFolders(ctx context.Context) ([]string, error) {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		m, err := s.runningManifest(ctx, r)
		if err != nil {
			continue
		}
		vars, folders, err := s.loadBinds(r.Name)
		if err != nil {
			continue
		}
		for _, b := range lenientBinds(m, vars, folders) {
			out = append(out, b.Host)
		}
	}
	return out, nil
}
