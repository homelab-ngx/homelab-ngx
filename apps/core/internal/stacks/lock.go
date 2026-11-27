package stacks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"filippo.io/age"

	"homelab-ngx.com/core/internal/agefile"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store"
)

// LockFile is the lock's name in the stacks state directory, which is inside
// the host's backups.
const LockFile = "stacks.lock.json"

// LockVersion is the lock format this daemon writes and reads.
const LockVersion = 1

// Lock is what a rebuilt host needs to get its stacks back: each stack's
// source, approved commit and approved manifest, the subnet it had and
// whether it was left running. Generations are not in it; a restored stack
// builds its approved commit again.
type Lock struct {
	Version int           `json:"version"`
	Stacks  []LockedStack `json:"stacks"`
}

// LockedStack is one stack in the lock. Only stacks with an approved commit
// are locked: one that was never approved has nothing to restore beyond its
// source, and is added again.
type LockedStack struct {
	Name        string          `json:"name"`
	Autostart   *bool           `json:"autostart,omitempty"`
	Label       string          `json:"label,omitempty"`
	MaskedUnits []string        `json:"masked_units,omitempty"`
	NixpkgsRev  string          `json:"nixpkgs_rev,omitempty"`
	Nixpkgs     string          `json:"nixpkgs,omitempty"`
	Source      string          `json:"source"`
	SSHKey      string          `json:"ssh_key,omitempty"`
	Rev         string          `json:"rev"`
	URL         string          `json:"url"`
	Manifest    json.RawMessage `json:"manifest"`
	Subnet      string          `json:"subnet"`
	WantRunning bool            `json:"want_running"`
}

func (s *Service) lockPath() string { return filepath.Join(s.host.StateDir, LockFile) }

// CurrentLock is the lock of the installed stacks.
func (s *Service) CurrentLock(ctx context.Context) (Lock, error) {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return Lock{}, err
	}
	l := Lock{Version: LockVersion, Stacks: []LockedStack{}}
	for _, r := range rows {
		if r.ApprovedRev == "" {
			continue
		}
		l.Stacks = append(l.Stacks, LockedStack{
			Name: r.Name, Label: r.Label, NixpkgsRev: r.NixpkgsRev, Nixpkgs: r.Nixpkgs, Source: r.Source, SSHKey: r.SshKey, Rev: r.ApprovedRev, URL: r.ApprovedUrl,
			Manifest: json.RawMessage(r.ApprovedManifest), Subnet: r.Subnet, WantRunning: r.WantRunning != 0,
			Autostart: new(r.Autostart != 0), MaskedUnits: storedMasks(r),
		})
	}
	return l, nil
}

// writeLock rewrites the lock file when the installed stacks changed. A
// failure is logged, not returned: the operation that changed the stacks
// succeeded, and the next change or monitor pass writes the lock again.
func (s *Service) writeLock(ctx context.Context) {
	l, err := s.CurrentLock(ctx)
	if err == nil {
		var b []byte
		if b, err = json.MarshalIndent(l, "", "  "); err == nil {
			b = append(b, '\n')
			if old, rerr := os.ReadFile(s.lockPath()); rerr == nil && bytes.Equal(old, b) {
				return
			}
			// Readable by the operator's CLI: it names repos and commits,
			// no secrets.
			err = writeFile(s.lockPath(), string(b), 0o644)
		}
	}
	if err != nil {
		s.log.Warn("stack lock not written", "path", s.lockPath(), "error", err)
	}
}

// RestoreLockFile reads stacks.lock.json from the stacks state directory and
// restores the stacks it lists.
func (s *Service) RestoreLockFile(ctx context.Context, ageKey string) (StackRestoreResult, error) {
	b, err := os.ReadFile(s.lockPath())
	if errors.Is(err, fs.ErrNotExist) {
		return StackRestoreResult{}, httpx.NotFound("no_host_lock", "no %s found in the host's stacks state directory", LockFile)
	}
	if err != nil {
		return StackRestoreResult{}, fmt.Errorf("reading %s: %w", s.lockPath(), err)
	}
	var l Lock
	if err := json.Unmarshal(b, &l); err != nil {
		return StackRestoreResult{}, badLock("%v", err)
	}
	return s.Restore(ctx, l, ageKey)
}

// Restore puts back every locked stack that is not installed: the approved
// commit and grants as they were and, when it is free, its subnet. When
// ageKey is given, variables.age files sealed to an old host's age key are
// re-sealed to this host's key for both newly restored and already installed
// stacks. Stacks that were running are started, which builds their approved
// commit.
func (s *Service) Restore(ctx context.Context, l Lock, ageKey string) (StackRestoreResult, error) {
	if l.Version != LockVersion {
		return StackRestoreResult{}, badLock("version %d is not supported (this daemon reads version %d)", l.Version, LockVersion)
	}
	var oldIDs []age.Identity
	if ageKey != "" {
		var err error
		oldIDs, err = agefile.ParseIdentities(ageKey)
		if err != nil {
			return StackRestoreResult{}, httpx.Unprocessable("invalid_age_key", "invalid age key: %v", err)
		}
	}
	res := StackRestoreResult{Restored: []string{}, Resealed: []string{}, Skipped: map[string]string{}}
	var toStart []string
	for _, e := range l.Stacks {
		if existing, gerr := s.st.Queries().GetStack(ctx, e.Name); gerr == nil {
			if len(oldIDs) > 0 {
				resealed, err := s.resealVarsFile(e.Name, oldIDs)
				if err != nil {
					res.Skipped[e.Name] = err.Error()
					continue
				}
				if resealed {
					res.Resealed = append(res.Resealed, e.Name)
					if running, err := s.runningManifest(ctx, existing); err == nil && running.Version > 0 {
						_ = s.renderVars(ctx, e.Name, running)
					}
					s.refreshHostIDs(ctx, e.Name)
					if existing.WantRunning != 0 && !s.isRunning(ctx, e.Name) {
						toStart = append(toStart, e.Name)
					}
					continue
				}
			}
			res.Skipped[e.Name] = "already installed"
			continue
		} else if !errors.Is(gerr, sql.ErrNoRows) {
			res.Skipped[e.Name] = gerr.Error()
			continue
		}
		resealed, err := s.restoreOne(ctx, e, oldIDs)
		if err != nil {
			res.Skipped[e.Name] = err.Error()
			continue
		}
		if resealed {
			res.Resealed = append(res.Resealed, e.Name)
		}
		res.Restored = append(res.Restored, e.Name)
		toStart = append(toStart, e.Name)
		s.log.Info("stack restored from the lock", "stack", e.Name, "rev", e.Rev)
	}
	s.writeLock(ctx)
	for _, name := range toStart {
		r, err := s.row(ctx, name)
		if err != nil || r.WantRunning == 0 {
			continue
		}
		if _, err := s.Start(ctx, name); err != nil {
			prefix := "restored but not started: "
			if !slices.Contains(res.Restored, name) {
				prefix = "re-sealed but not started: "
			}
			res.Skipped[name] = prefix + err.Error()
		}
	}
	return res, nil
}

func (s *Service) restoreOne(ctx context.Context, e LockedStack, oldIDs []age.Identity) (bool, error) {
	if !ValidName(e.Name) {
		return false, fmt.Errorf("%q is not a stack name", e.Name)
	}
	if e.Rev == "" || e.URL == "" || e.Source == "" {
		return false, errors.New("source, rev and url are required")
	}
	m, err := ParseManifest(e.Manifest)
	if err != nil {
		return false, fmt.Errorf("manifest: %w", err)
	}
	if err := hostUnitsClash(e.Name, m); err != nil {
		return false, err
	}
	label, err := validLabel(e.Label)
	if err != nil {
		return false, err
	}
	if e.Nixpkgs != "" && (m.Version < 7 || !ValidNixpkgs(e.Nixpkgs)) {
		return false, fmt.Errorf("nixpkgs %q is not a nixpkgs choice for this manifest", e.Nixpkgs)
	}
	if e.NixpkgsRev != "" && (m.Version < 7 || !commitRE.MatchString(e.NixpkgsRev)) {
		return false, fmt.Errorf("nixpkgs_rev %q is not a nixpkgs commit for this manifest", e.NixpkgsRev)
	}
	if _, err := s.st.Queries().GetStack(ctx, e.Name); err == nil {
		return false, errors.New("already installed")
	}
	if err := s.portClash(ctx, e.Name, m); err != nil {
		return false, err
	}
	var resealed bool
	if len(oldIDs) > 0 {
		var rerr error
		resealed, rerr = s.resealVarsFile(e.Name, oldIDs)
		if rerr != nil {
			return false, rerr
		}
	}
	subnet := e.Subnet
	if _, _, err := addrs(subnet); err != nil {
		subnet = ""
	}
	// A restore is a first boot for the stacks: each one starts when it
	// starts at boot. A lock from before autostart has want_running only.
	auto := e.WantRunning
	if e.Autostart != nil {
		auto = *e.Autostart
	}
	want := int64(0)
	if auto {
		want = 1
	}
	err = s.st.Tx(ctx, func(q *store.Queries) error {
		rows, err := q.ListStacks(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if subnet != "" && r.Subnet == subnet {
				subnet = "" // another stack has it now; start allocates one
			}
		}
		return q.RestoreStack(ctx, store.RestoreStackParams{
			Name: e.Name, Source: e.Source, Now: s.now().UnixMilli(),
			Rev: e.Rev, Url: e.URL, Manifest: encode(m), WantRunning: want, Autostart: want, Subnet: subnet, Label: label, NixpkgsRev: e.NixpkgsRev, Nixpkgs: e.Nixpkgs,
			MaskedUnits: encodeMasks(inOrder(m.Health.Units, e.MaskedUnits)), SshKey: e.SSHKey,
		})
	})
	if err != nil {
		return false, err
	}
	// The lock holds no token; an API grant gets a new one.
	s.syncAPITokenLogged(ctx, e.Name)
	// The restore approves the locked manifest again. Paths kept in the
	// restored variables.age stay as they are.
	s.materializePathsLogged(e.Name, m)
	return resealed, nil
}

func badLock(f string, a ...any) *httpx.Problem {
	return httpx.Unprocessable("invalid_lock", "stack lock: "+f, a...)
}
