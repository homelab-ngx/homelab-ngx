package storage

import (
	"bufio"
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// releaseTries and releaseWait are how often, and how far apart, an
// unmount that is busy is tried at a shutdown.
const (
	releaseTries = 3
	releaseWait  = 2 * time.Second
)

// Mounted lists what the daemon mounted that is mounted now: the pools and
// the partitions under MountRoot, and a pool stored with a mountpoint of its
// own. The deepest come first, so each is unmounted before what holds it.
func (s *Service) Mounted() []string { return s.mountedIn("/proc/self/mountinfo") }

func (s *Service) mountedIn(mountinfo string) []string {
	ours := map[string]bool{}
	for _, p := range s.cfg.Current().Pools {
		ours[Canonical(p.Mountpoint)] = true
	}
	f, err := os.Open(mountinfo)
	if err != nil {
		s.log.Warn("cannot read the mounts", "error", err)
		return nil
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		mp := unescapeMount(fields[4])
		if seen[mp] || !(ours[mp] || strings.HasPrefix(mp, MountRoot+"/")) {
			continue
		}
		seen[mp] = true
		out = append(out, mp)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Count(out[i], "/") > strings.Count(out[j], "/")
	})
	return out
}

// unescapeMount undoes mountinfo's octal escapes (\040 is a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Release unmounts mountpoint for a shutdown. Unlike Unmount it records
// nothing, so the next boot mounts it again. A mount still in use is tried
// a few times; the error that is left says why.
func (s *Service) Release(ctx context.Context, mountpoint string) error {
	return s.release(ctx, mountpoint, releaseWait)
}

func (s *Service) release(ctx context.Context, mountpoint string, wait time.Duration) error {
	s.invalidate()
	defer s.invalidate()
	var err error
	for try := 0; try < releaseTries; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		if err = s.bc.Unmount(ctx, mountpoint); err == nil {
			s.guard(ctx, mountpoint)
			return nil
		}
	}
	return err
}

// Restore mounts again what a shutdown that was called off unmounted:
// every pool and partition set to mount at boot.
func (s *Service) Restore(ctx context.Context) { s.AutoMount(ctx) }
