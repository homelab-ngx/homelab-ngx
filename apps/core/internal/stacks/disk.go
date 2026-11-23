package stacks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// A service's disk use is the bytes its unit's user owns in the stack's
// root. The host walks the root from outside the container, where the
// stack's binds (/nix/store, its path variables) are not mounted, so they are
// never counted. A unit that runs as root has no figure: root owns the
// stack's system files too.
const (
	diskEvery  = 7 * 24 * time.Hour
	diskSettle = 10 * time.Minute // after the daemon starts, before the first look
	diskCheck  = time.Hour        // how often the loop looks for a stack that is due
)

// diskRecord is what the last measurement of one stack found: bytes per
// unit, only for units whose user owns files.
type diskRecord struct {
	MeasuredAt time.Time        `json:"measured_at"`
	Units      map[string]int64 `json:"units"`
}

// WatchDisk measures every stack's services once a week, until ctx ends. A
// stack is measured while it runs and is not frozen: the units' users are
// read from its systemd. Updates and rollbacks measure at once (finish).
func (s *Service) WatchDisk(ctx context.Context) {
	if s.host.DiskFile == "" {
		return
	}
	t := time.NewTimer(diskSettle)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rows, err := s.st.Queries().ListStacks(ctx)
		if err == nil {
			for _, r := range rows {
				if s.diskDue(r.Name) {
					s.measureDisk(ctx, r.Name)
				}
			}
		}
		t.Reset(diskCheck)
	}
}

// measureDiskSoon measures a stack's services in the background, after a
// build changed what runs in it.
func (s *Service) measureDiskSoon(name string) {
	if s.host.DiskFile == "" {
		return
	}
	s.mu.Lock()
	ctx := s.ctx
	s.mu.Unlock()
	go s.measureDisk(ctx, name)
}

// diskDue reports whether a stack has no measurement younger than a week.
func (s *Service) diskDue(name string) bool {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	rec, ok := s.diskRecordsLocked()[name]
	return !ok || s.now().Sub(rec.MeasuredAt) >= diskEvery
}

// measureDisk walks one stack's root and records each unit's bytes. A
// stack that is stopped or frozen keeps its last figures.
func (s *Service) measureDisk(ctx context.Context, name string) {
	s.diskWalk.Lock()
	defer s.diskWalk.Unlock()
	r, err := s.row(ctx, name)
	if err != nil {
		return
	}
	g, err := s.generation(ctx, r)
	if err != nil || g == nil {
		return
	}
	m, err := decode(g.Manifest)
	if err != nil || len(m.Health.Units) == 0 {
		return
	}
	if !s.isRunning(ctx, name) || s.frozen(ctx, name) {
		return
	}
	users := s.unitUsers(ctx, name, m.Health.Units)
	root := filepath.Join(s.stateDir(name), "root")
	byUID, base, err := ownedBytes(root)
	if err != nil {
		s.log.Warn("a stack's services were not measured", "stack", name, "error", err)
		return
	}
	passwd, err := readRegular(filepath.Join(root, "etc", "passwd"))
	if err != nil {
		s.log.Warn("a stack's users were not read", "stack", name, "error", err)
		return
	}
	units := unitDisk(byUID, base, passwd, users)
	s.diskMu.Lock()
	recs := s.diskRecordsLocked()
	recs[name] = diskRecord{MeasuredAt: s.now(), Units: units}
	s.saveDiskLocked(recs)
	s.diskMu.Unlock()
}

// unitUsers reads the User= of units inside a running stack in one call. A
// unit with no user runs as root and is left out.
func (s *Service) unitUsers(ctx context.Context, stack string, units []string) map[string]string {
	args := append([]string{"--machine=" + stack, "show", "--property=Id,User", "--"}, units...)
	res, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "stacks", Name: "systemctl", Args: args})
	out := map[string]string{}
	if err != nil {
		return out
	}
	for _, block := range strings.Split(res.Stdout, "\n\n") {
		p := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				p[k] = v
			}
		}
		if p["Id"] != "" && p["User"] != "" {
			out[p["Id"]] = p["User"]
		}
	}
	return out
}

// ownedBytes adds up the space allocated to files under root by owner, as
// du counts it: allocated blocks, a hard-linked file once, nothing on another
// file system. base is the owner of root itself, the uid the container's
// root has on disk (0 when the root is idmapped).
func ownedBytes(root string) (byUID map[uint32]int64, base uint32, err error) {
	fi, err := os.Lstat(root)
	if err != nil {
		return nil, 0, err
	}
	top, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, 0, errors.New("no owner for " + root)
	}
	dev, base := uint64(top.Dev), uint32(top.Uid)
	byUID = map[uint32]int64{}
	seen := map[uint64]bool{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a file gone or unreadable is left out
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if uint64(st.Dev) != dev {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() && uint64(st.Nlink) > 1 {
			if seen[uint64(st.Ino)] {
				return nil
			}
			seen[uint64(st.Ino)] = true
		}
		byUID[uint32(st.Uid)] += int64(st.Blocks) * 512
		return nil
	})
	return byUID, base, err
}

// unitDisk gives each unit the bytes its user owns. users maps a unit to its
// User= (a name or a number); passwd is the stack's /etc/passwd. Root and
// users passwd does not know are left out.
func unitDisk(byUID map[uint32]int64, base uint32, passwd []byte, users map[string]string) map[string]int64 {
	uids := map[string]uint32{}
	sc := bufio.NewScanner(bytes.NewReader(passwd))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ":")
		if len(f) < 3 {
			continue
		}
		if n, err := strconv.ParseUint(f[2], 10, 32); err == nil {
			uids[f[0]] = uint32(n)
		}
	}
	out := map[string]int64{}
	for unit, user := range users {
		uid, ok := uids[user]
		if n, err := strconv.ParseUint(user, 10, 32); err == nil {
			uid, ok = uint32(n), true
		}
		if !ok || uid == 0 {
			continue
		}
		out[unit] = byUID[base+uid]
	}
	return out
}

// readRegular reads a file that must be a regular file, not a link: a link
// in a stack's root points into the stack, not into the host.
func readRegular(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New(path + " is not a regular file")
	}
	return os.ReadFile(path)
}

// diskOf is a unit's last measured bytes; nil when there is none.
func (s *Service) diskOf(stack, unit string) *int64 {
	if s.host.DiskFile == "" {
		return nil
	}
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	n, ok := s.diskRecordsLocked()[stack].Units[unit]
	if !ok {
		return nil
	}
	return &n
}

func (s *Service) diskRecordsLocked() map[string]diskRecord {
	if s.diskRecs == nil {
		s.diskRecs = map[string]diskRecord{}
		if b, err := os.ReadFile(s.host.DiskFile); err == nil {
			_ = json.Unmarshal(b, &s.diskRecs)
		}
	}
	return s.diskRecs
}

func (s *Service) saveDiskLocked(recs map[string]diskRecord) {
	s.diskRecs = recs
	b, err := json.Marshal(recs)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(s.host.DiskFile), 0o750); err == nil {
			err = os.WriteFile(s.host.DiskFile, b, 0o640)
		}
	}
	if err != nil {
		s.log.Warn("the services' disk record was not saved", "error", err)
	}
}

// forgetDisk drops a removed stack's measurement.
func (s *Service) forgetDisk(name string) {
	if s.host.DiskFile == "" {
		return
	}
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	recs := s.diskRecordsLocked()
	if _, ok := recs[name]; ok {
		delete(recs, name)
		s.saveDiskLocked(recs)
	}
}
