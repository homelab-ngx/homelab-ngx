package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/storage/bcachefs"
)

// Making, growing, shrinking and deleting a pool. The removal of a disk runs
// in the background (operation.go); the rest answers when it is done.

var (
	// ErrBusy is a change to a pool while a disk is being removed from it.
	ErrBusy = errors.New("a disk is being removed from the pool")
	// ErrPoolInUse is a delete of a pool whose folders a stack or backup uses.
	ErrPoolInUse = errors.New("the pool is in use")
	// ErrDiskNotEligible is a disk a pool cannot take: the system disk, one
	// with something mounted, or a member of another pool.
	ErrDiskNotEligible = errors.New("the disk cannot go into a pool")
	// ErrUnknownDisk is a disk that is not in the pool.
	ErrUnknownDisk = errors.New("the pool has no such disk")
	// ErrPoolState is a change that needs the pool mounted.
	ErrPoolState = errors.New("the pool is not mounted")
	// ErrTooFewDisks is fewer disks than copies.
	ErrTooFewDisks = errors.New("fewer disks than copies")
	// ErrLastOfRole is removing the only SSD or the only HDD of a pool that
	// uses both.
	ErrLastOfRole = errors.New("the pool needs this disk")
	// ErrNoRoom is removing a disk whose data does not fit on the others.
	ErrNoRoom = errors.New("the other disks do not have the room")
	// ErrPoolDegraded is a change to a pool with a disk missing.
	ErrPoolDegraded = errors.New("a disk of the pool is missing")
	// ErrImageTooOld is an image whose bcachefs tools are older than the
	// daemon's pool commands.
	ErrImageTooOld = errors.New("this image's bcachefs tools are too old to change a pool: update the system")
	// ErrConfirm is a change that erases something, without its confirmation.
	ErrConfirm = errors.New("not confirmed")
)

// Users tells which stacks and backups keep folders under a mountpoint.
type Users interface {
	PoolUsers(ctx context.Context, mountpoint string) ([]string, error)
}

// UseUsers makes a pool that u names a user of impossible to delete.
func (s *Service) UseUsers(u Users) { s.users = u }

// MaxCopies is the most copies a pool is made with.
const MaxCopies = 3

// roomMargin is how full the remaining disks may get when one leaves.
const roomMargin = 0.85

// diskKind is ssd or hdd: what the disk's label says in a pool made here,
// else what the disk is.
func diskKind(d bcachefs.Disk) string {
	if g := labelGroup(d.BcachefsLabel); g == bcachefs.KindSSD || g == bcachefs.KindHDD {
		return g
	}
	return bcachefs.Kind(d.Rotational)
}

func labelGroup(label string) string {
	group, _, _ := strings.Cut(label, ".")
	return group
}

// nextLabel is the first unused label of a kind: hdd.hdd3 beside hdd1 and hdd2.
func nextLabel(kind string, taken []string) string {
	for n := 1; ; n++ {
		l := kind + "." + kind + strconv.Itoa(n)
		if !slices.Contains(taken, l) {
			return l
		}
	}
}

// spareDisks are the disks a pool can be made from: the candidates that no
// discovered pool has a member on.
func (s *Service) spareDisks(ctx context.Context, pools []bcachefs.Pool) ([]bcachefs.Candidate, error) {
	cands, err := s.bc.Candidates(ctx)
	if err != nil {
		return nil, err
	}
	member := map[string]bool{}
	for _, p := range pools {
		for _, d := range p.Disks {
			member[d.Path] = true
		}
	}
	out := cands[:0]
	for _, c := range cands {
		if member[c.Path] || slices.ContainsFunc(c.Parts, func(p string) bool { return member[p] }) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *Service) checkTools(ctx context.Context) error {
	major, minor, err := s.bc.ToolsVersion(ctx)
	if err != nil {
		return err
	}
	if major < bcachefs.MinTools[0] || (major == bcachefs.MinTools[0] && minor < bcachefs.MinTools[1]) {
		return fmt.Errorf("%w (bcachefs-tools %d.%d, needs %d.%d)", ErrImageTooOld, major, minor, bcachefs.MinTools[0], bcachefs.MinTools[1])
	}
	return nil
}

// take checks that every path is a spare disk and that the ones with contents
// are confirmed by name, then wipes those.
func (s *Service) take(ctx context.Context, pools []bcachefs.Pool, paths []string, name, confirm string) ([]bcachefs.Candidate, error) {
	spare, err := s.spareDisks(ctx, pools)
	if err != nil {
		return nil, err
	}
	var picked []bcachefs.Candidate
	for _, path := range paths {
		i := slices.IndexFunc(spare, func(c bcachefs.Candidate) bool { return c.Path == path })
		if i < 0 {
			return nil, fmt.Errorf("%w: %s", ErrDiskNotEligible, path)
		}
		picked = append(picked, spare[i])
	}
	for _, c := range picked {
		if c.Contents != "" && confirm != name {
			return nil, fmt.Errorf("%w: %s holds %s; send the pool's name as confirm to erase it", ErrConfirm, c.Path, c.Contents)
		}
	}
	for _, c := range picked {
		if c.Contents == "" {
			continue
		}
		if err := s.bc.Wipe(ctx, c.Path, c.Parts); err != nil {
			return nil, err
		}
	}
	return picked, nil
}

// CreatePool makes a pool from spare disks, mounts it at MountRoot/<name>
// and remembers it. With SSDs and HDDs together the SSDs take new writes,
// metadata and cached reads, and the data moves to the HDDs.
func (s *Service) CreatePool(ctx context.Context, name string, disks []string, copies int, confirm string) (Pool, error) {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	if !mountNameRE.MatchString(name) {
		return Pool{}, ErrMountName
	}
	disks = slices.Compact(slices.Sorted(slices.Values(disks)))
	if copies < 1 || copies > MaxCopies || len(disks) == 0 {
		return Pool{}, fmt.Errorf("%w: 1 to %d copies", ErrTooFewDisks, MaxCopies)
	}
	if copies > len(disks) {
		return Pool{}, fmt.Errorf("%w: %d copies need %d disks", ErrTooFewDisks, copies, copies)
	}
	if err := s.checkTools(ctx); err != nil {
		return Pool{}, err
	}
	pools, err := s.bc.DiscoverPools(ctx)
	if err != nil {
		return Pool{}, err
	}
	mountpoint := Mountpoint(name)
	if s.nameTaken(ctx, name, "") || slices.ContainsFunc(pools, func(p bcachefs.Pool) bool { return p.Mountdir == mountpoint }) {
		return Pool{}, fmt.Errorf("%w: %s", ErrNameTaken, name)
	}
	picked, err := s.take(ctx, pools, disks, name, confirm)
	if err != nil {
		return Pool{}, err
	}
	defer s.invalidate()

	spec := bcachefs.FormatSpec{Name: name, Copies: copies}
	var labels []string
	kinds := map[string]bool{}
	for _, c := range picked {
		kind := bcachefs.Kind(c.Rotational)
		kinds[kind] = true
		label := nextLabel(kind, labels)
		labels = append(labels, label)
		spec.Devices = append(spec.Devices, bcachefs.FormatDevice{Path: c.Path, Label: label})
	}
	spec.Tiered = len(kinds) == 2
	if err := s.bc.Format(ctx, spec); err != nil {
		return Pool{}, err
	}
	if err := s.open(ctx, mountpoint); err != nil {
		return Pool{}, err
	}
	if err := s.bc.Mount(ctx, disks, mountpoint); err != nil {
		s.guard(ctx, mountpoint)
		return Pool{}, err
	}
	s.rememberMount(ctx, mountpoint)
	s.invalidate()
	st, err := s.Status(ctx)
	if err != nil {
		return Pool{}, err
	}
	for _, p := range st.Pools {
		if p.Mountdir == mountpoint {
			return p, nil
		}
	}
	return Pool{}, fmt.Errorf("the pool was made but is not listed at %s", mountpoint)
}

// mountedPool finds a mounted pool that is whole and has no removal running.
func (s *Service) mountedPool(ctx context.Context, uuid string) (bcachefs.Pool, []bcachefs.Pool, []bcachefs.DeviceUsage, error) {
	pools, err := s.bc.DiscoverPools(ctx)
	if err != nil {
		return bcachefs.Pool{}, nil, nil, err
	}
	i := slices.IndexFunc(pools, func(p bcachefs.Pool) bool { return p.UUID == uuid })
	if i < 0 {
		return bcachefs.Pool{}, nil, nil, fmt.Errorf("%w: %q", ErrUnknownPool, uuid)
	}
	p := pools[i]
	if p.State != "mounted" || p.Mountdir == "" {
		return p, pools, nil, ErrPoolState
	}
	if s.busy(uuid) {
		return p, pools, nil, ErrBusy
	}
	usage, err := s.bc.DeviceUsage(ctx, p.Mountdir)
	if err != nil {
		return p, pools, nil, err
	}
	// Every member the pool counts must be a disk that is here.
	for _, u := range usage {
		if !slices.ContainsFunc(p.Disks, func(d bcachefs.Disk) bool { return d.Name == u.Name }) {
			return p, pools, nil, fmt.Errorf("%w: %s", ErrPoolDegraded, u.Name)
		}
	}
	return p, pools, usage, nil
}

// storedName is the name a pool goes by on the Storage page.
func (s *Service) storedName(p bcachefs.Pool) string {
	snap := s.cfg.Current()
	stored := map[string]config.Pool{}
	for _, sp := range snap.Pools {
		stored[sp.UUID] = sp
	}
	return poolName(p, stored)
}

// AddDisk adds a spare disk to a mounted pool. A pool that gets its first
// SSD beside HDDs, or the reverse, gets the targets a pool made that way has.
func (s *Service) AddDisk(ctx context.Context, uuid, disk, confirm string) error {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	if err := s.checkTools(ctx); err != nil {
		return err
	}
	pool, pools, _, err := s.mountedPool(ctx, uuid)
	if err != nil {
		return err
	}
	picked, err := s.take(ctx, pools, []string{disk}, s.storedName(pool), confirm)
	if err != nil {
		return err
	}
	defer s.invalidate()

	kind := bcachefs.Kind(picked[0].Rotational)
	var labels []string
	groups := map[string]bool{kind: true}
	ours := true // every member is labelled the way CreatePool labels
	for _, d := range pool.Disks {
		labels = append(labels, d.BcachefsLabel)
		g := labelGroup(d.BcachefsLabel)
		groups[g] = true
		if g != bcachefs.KindSSD && g != bcachefs.KindHDD {
			ours = false
		}
	}
	if err := s.bc.AddDevice(ctx, pool.Mountdir, disk, nextLabel(kind, labels)); err != nil {
		return err
	}
	untargeted := pool.ForegroundTarget == "" && pool.BackgroundTarget == "" && pool.PromoteTarget == "" && pool.MetadataTarget == ""
	if ours && untargeted && groups[bcachefs.KindSSD] && groups[bcachefs.KindHDD] {
		if err := s.bc.SetTargets(ctx, pool.Disks[0].Path); err != nil {
			return fmt.Errorf("the disk is in the pool, but its SSDs are not set to cache: %w", err)
		}
	}
	return nil
}

// usableRaw is how many bytes disks of these sizes hold when every piece of
// data is kept on `copies` different disks: one disk larger than the others
// together cannot be filled.
func usableRaw(sizes []uint64, copies int) uint64 {
	if copies < 1 || len(sizes) < copies {
		return 0
	}
	var each uint64 // the most any one disk can hold of the data
	for _, sz := range sizes {
		each = max(each, sz)
	}
	for range 64 {
		var sum uint64
		for _, sz := range sizes {
			sum += min(sz, each)
		}
		next := sum / uint64(copies)
		if next >= each {
			break
		}
		each = next
	}
	var sum uint64
	for _, sz := range sizes {
		sum += min(sz, each)
	}
	return sum
}

// removable says why a member cannot leave the pool; nil when it can.
func removable(p bcachefs.Pool, usage []bcachefs.DeviceUsage, disk bcachefs.Disk) error {
	copies := max(p.DataReplicas, p.MetadataReplicas, 1)
	if len(p.Disks)-1 < copies {
		return fmt.Errorf("%w: the pool keeps %d copies and would have %d disks", ErrTooFewDisks, copies, len(p.Disks)-1)
	}
	group := labelGroup(disk.BcachefsLabel)
	targeted := slices.Contains([]string{p.ForegroundTarget, p.BackgroundTarget, p.PromoteTarget, p.MetadataTarget}, group)
	if group != "" && targeted {
		others := 0
		for _, d := range p.Disks {
			if d.Path != disk.Path && labelGroup(d.BcachefsLabel) == group {
				others++
			}
		}
		if others == 0 {
			return fmt.Errorf("%w: it is the pool's only %s disk", ErrLastOfRole, group)
		}
	}
	var used uint64
	var rest []uint64
	for _, u := range usage {
		used += u.Used
		if u.Name != disk.Name {
			rest = append(rest, u.Size)
		}
	}
	if room := usableRaw(rest, copies); float64(used) > roomMargin*float64(room) {
		return fmt.Errorf("%w: the pool holds %d bytes and the other disks take %d", ErrNoRoom, used, room)
	}
	return nil
}

// RemoveDisk starts taking a disk out of a mounted pool: its data moves to
// the other disks, then it leaves the pool and is wiped. It returns once the
// removal runs; the pool's operation shows how far it is.
func (s *Service) RemoveDisk(ctx context.Context, uuid, disk string) (Pool, error) {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	if err := s.checkTools(ctx); err != nil {
		return Pool{}, err
	}
	pool, _, usage, err := s.mountedPool(ctx, uuid)
	if err != nil {
		return Pool{}, err
	}
	i := slices.IndexFunc(pool.Disks, func(d bcachefs.Disk) bool { return d.Path == disk })
	if i < 0 {
		return Pool{}, fmt.Errorf("%w: %s", ErrUnknownDisk, disk)
	}
	member := pool.Disks[i]
	if err := removable(pool, usage, member); err != nil {
		return Pool{}, err
	}
	rec := removal{PoolUUID: uuid, Mountpoint: pool.Mountdir, Disk: member.Path, Name: member.Name, Serial: member.Serial, StartedAt: s.now()}
	for _, u := range usage {
		if u.Name == member.Name {
			rec.Total = u.Used
		}
	}
	s.start(ctx, rec)
	s.invalidate()
	st, err := s.Status(ctx)
	if err != nil {
		return Pool{}, err
	}
	for _, p := range st.Pools {
		if p.Uuid == uuid {
			return p, nil
		}
	}
	return Pool{}, fmt.Errorf("%w: %q", ErrUnknownPool, uuid)
}

// DeletePool unmounts a pool, wipes its disks and forgets it. confirm is the
// pool's name. It is refused while a stack or a backup keeps a folder on it.
func (s *Service) DeletePool(ctx context.Context, uuid, confirm string) error {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	pools, err := s.bc.DiscoverPools(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(pools, func(p bcachefs.Pool) bool { return p.UUID == uuid })
	if i < 0 {
		return fmt.Errorf("%w: %q", ErrUnknownPool, uuid)
	}
	pool := pools[i]
	if s.busy(uuid) {
		return ErrBusy
	}
	name := s.storedName(pool)
	if confirm != name {
		return fmt.Errorf("%w: send the pool's name, %s, as confirm; every disk in it is erased", ErrConfirm, name)
	}
	mountpoint := pool.Mountdir
	if sp, ok := s.cfg.Current().Pool(uuid); ok && mountpoint == "" {
		mountpoint = sp.Mountpoint
	}
	if s.users != nil && mountpoint != "" {
		users, err := s.users.PoolUsers(ctx, mountpoint)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return fmt.Errorf("%w by %s", ErrPoolInUse, strings.Join(users, ", "))
		}
	}
	defer s.invalidate()
	s.invalidate()
	if pool.State == "mounted" {
		if err := s.bc.Unmount(ctx, pool.Mountdir); err != nil {
			return err
		}
	}
	for _, d := range pool.Disks {
		if err := s.bc.Wipe(ctx, d.Path, nil); err != nil {
			return err
		}
	}
	if _, err := s.cfg.DeletePool(ctx, uuid); err != nil {
		return err
	}
	s.forgetOperation(uuid)
	if _, ok := MountName(mountpoint); ok {
		// The directory is the pool's own: lift the guard an unmounted one
		// has, and take it away so the name is free.
		if err := s.bc.ClearImmutable(ctx, mountpoint); err != nil {
			s.log.Debug("chattr -i failed (directory may not be immutable)", "mountpoint", mountpoint, "error", err)
		}
		if err := s.rmdir(mountpoint); err != nil {
			s.log.Warn("the deleted pool's directory stays", "mountpoint", mountpoint, "error", err)
		}
	}
	if mountpoint != "" {
		s.dataMu.Lock()
		changed := false
		for path := range s.sizes {
			if strings.HasPrefix(path, filepath.Clean(mountpoint)+"/") {
				delete(s.sizes, path)
				changed = true
			}
		}
		if changed {
			s.saveSizesLocked(ctx)
		}
		s.dataMu.Unlock()
	}
	return nil
}
