// Package storage manages the bcachefs pools: the cached status view, mount
// and unmount (remembering which pools to mount at boot), the boot auto-mount
// routine and subvolume operations. Command execution lives in the bcachefs
// subpackage.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/storage/bcachefs"
)

// statusTTL coalesces concurrent tabs and back-to-back polls so they don't
// hammer the disk-level bcachefs CLI. It stays below the page's 10s poll so
// each poll sees a fresh compute.
const statusTTL = 5 * time.Second

// statusComputeTimeout bounds the shared compute, which runs detached from any
// one caller's cancellation, so a hung CLI call cannot hold every caller.
const statusComputeTimeout = time.Minute

const statusKey = "status"

// healthInterval is how often every disk's SMART reading is refreshed.
// smartctl runs with -n standby, so a sleeping disk is skipped, not woken.
const healthInterval = 10 * time.Minute

// sizeInterval is how old a folder's measured size may get before the
// background loop measures it again. A restart re-measures only stale ones.
// A week: every run reads the pool's whole extents and reflink btrees, and
// the operator can measure on demand from the Storage page.
const sizeInterval = 7 * 24 * time.Hour

// sizesKey is the meta row that keeps measured folder sizes across restarts.
const sizesKey = "storage.folder_sizes"

// MetaStore keeps small values across restarts; *store.Store satisfies it.
type MetaStore interface {
	Meta(ctx context.Context, key string) (string, bool, error)
	SetMeta(ctx context.Context, key, value string) error
}

type folderSize struct {
	Bytes      int64     `json:"bytes"`
	MeasuredAt time.Time `json:"measured_at"`
}

// ErrUnknownPool means the uuid is not a stored pool.
var ErrUnknownPool = errors.New("unknown pool")

// MountRoot is the one directory pools mount under, each at
// MountRoot/<name>. It is on /var: the root file system is read-only.
const MountRoot = "/var/mnt"

// DefaultMountName is the name of a pool mounted without one.
const DefaultMountName = "pool"

var mountNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// ErrMountName is a pool name that cannot be a directory under MountRoot.
var ErrMountName = errors.New("a pool's name is up to 31 lowercase letters, digits and dashes, and starts with a letter or a digit")

// ErrNameTaken is a mount under a name another pool has.
var ErrNameTaken = errors.New("another pool has that name")

// Canonical is path with the image's /mnt link (to MountRoot) written out,
// so a path typed either way is stored one way.
func Canonical(path string) string {
	if rest, ok := strings.CutPrefix(path, "/mnt/"); ok {
		return MountRoot + "/" + rest
	}
	return path
}

// Mountpoint is where the pool named name mounts.
func Mountpoint(name string) string { return MountRoot + "/" + name }

// MountName is the name of a pool mounted at mountpoint; false when
// mountpoint is not a directory directly under MountRoot.
func MountName(mountpoint string) (string, bool) {
	name, ok := strings.CutPrefix(mountpoint, MountRoot+"/")
	if !ok || !mountNameRE.MatchString(name) {
		return "", false
	}
	return name, true
}

// ErrSubvolumeNotEmpty is returned when deleting a subvolume that still holds
// data.
var ErrSubvolumeNotEmpty = bcachefs.ErrSubvolumeNotEmpty

// Service is the storage domain.
type Service struct {
	bc  *bcachefs.Client
	cfg *config.Manager
	log *slog.Logger

	mu       sync.Mutex
	cache    *StorageStatus
	deadline time.Time
	gen      uint64
	flight   singleflight.Group

	// Background readings, merged into every status compute.
	meta      MetaStore
	dataMu    sync.Mutex
	health    map[string]DiskHealth // by device path
	sizes     map[string]folderSize // by subvolume path
	measureMu sync.Mutex            // one bcdu run at a time
	trees     map[string]folderTree // browse figures, by browse root
	folders   FolderSource          // the stacks' host folders; may be nil

	// Seams overridden by tests.
	now             func() time.Time
	ttl             time.Duration
	discoverTimeout time.Duration
	retryInterval   time.Duration
	onBcachefs      func(path string) bool
	mkdir           func(path string) error
}

// New wires a Service.
func New(bc *bcachefs.Client, cfg *config.Manager, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		bc: bc, cfg: cfg, log: log,
		health: map[string]DiskHealth{}, sizes: map[string]folderSize{}, trees: map[string]folderTree{},
		now: time.Now, ttl: statusTTL, onBcachefs: bc.OnBcachefs,
		mkdir:           func(path string) error { return os.MkdirAll(path, 0o755) },
		discoverTimeout: 2 * time.Minute, retryInterval: 5 * time.Second,
	}
}

// UseStore keeps measured folder sizes in m across restarts.
func (s *Service) UseStore(m MetaStore) { s.meta = m }

func (s *Service) invalidate() {
	s.mu.Lock()
	s.cache = nil
	s.gen++
	s.flight.Forget(statusKey)
	s.mu.Unlock()
}

// ---- status ----

// Status returns pools, unassigned disks and subvolumes, cached for statusTTL.
// Concurrent misses share one compute, which runs detached from the first
// caller's cancellation so one aborted request does not fail the others.
func (s *Service) Status(ctx context.Context) (StorageStatus, error) {
	s.mu.Lock()
	if s.cache != nil && s.now().Before(s.deadline) {
		out := *s.cache
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()

	ch := s.flight.DoChan(statusKey, func() (any, error) {
		s.mu.Lock()
		gen := s.gen
		s.mu.Unlock()
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), statusComputeTimeout)
		defer cancel()
		st, err := s.compute(cctx)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		if s.gen == gen {
			s.cache, s.deadline = &st, s.now().Add(s.ttl)
		}
		s.mu.Unlock()
		return st, nil
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return StorageStatus{}, r.Err
		}
		return r.Val.(StorageStatus), nil
	case <-ctx.Done():
		return StorageStatus{}, ctx.Err()
	}
}

func poolName(p bcachefs.Pool, stored map[string]config.Pool) string {
	if sp, ok := stored[p.UUID]; ok && sp.Name != "" {
		return sp.Name
	}
	if p.Label != "" {
		return p.Label
	}
	if len(p.UUID) >= 8 {
		return "pool-" + p.UUID[:8]
	}
	return "pool-" + p.UUID
}

func (s *Service) compute(ctx context.Context) (StorageStatus, error) {
	snap := s.cfg.Current()
	stored := make(map[string]config.Pool, len(snap.Pools))
	for _, p := range snap.Pools {
		stored[p.UUID] = p
	}

	pools, err := s.bc.DiscoverPools(ctx)
	if err != nil {
		s.log.Error("DiscoverPools failed", "error", err)
		return StorageStatus{}, err
	}

	var diskNames []string
	for i := range pools {
		p := &pools[i]
		p.Name = poolName(*p, stored)
		if p.State == "mounted" && p.Mountdir != "" {
			if u, err := s.bc.GetPoolUsage(ctx, p.Mountdir); err == nil {
				p.Usage = u
			} else {
				s.log.Error("failed to get pool usage", "mountdir", p.Mountdir, "error", err)
			}
		}
		for _, d := range p.Disks {
			if d.Name != "" {
				diskNames = append(diskNames, d.Name)
			}
		}
	}
	if len(diskNames) > 0 {
		if io, err := s.bc.GetDiskIO(ctx, diskNames); err == nil {
			for i := range pools {
				for j := range pools[i].Disks {
					d := &pools[i].Disks[j]
					if st, ok := io[d.Name]; ok {
						d.IO = &st
					}
				}
			}
		} else {
			s.log.Error("failed to get disk I/O stats", "error", err)
		}
	}

	unassigned, err := s.bc.GetUnassignedDisks(ctx)
	if err != nil {
		s.log.Error("GetUnassignedDisks failed", "error", err)
		return StorageStatus{}, err
	}

	subvolumes := []Subvolume{}
	for _, p := range pools {
		if p.State == "mounted" && p.Mountdir != "" {
			if subs, err := s.bc.ListSubvolumes(ctx, p.Mountdir); err == nil {
				for _, sv := range subs {
					subvolumes = append(subvolumes, Subvolume{Id: sv.ID, Path: sv.Path, UsedBytes: sv.UsedBytes})
				}
			}
		}
	}

	out := StorageStatus{
		Pools:      make([]Pool, 0, len(pools)),
		Unassigned: make([]Disk, 0, len(unassigned)),
		Subvolumes: subvolumes,
	}
	for _, p := range pools {
		pool := toPool(p, stored[p.UUID].AutoMount)
		if pool.MountName, _ = MountName(stored[p.UUID].Mountpoint); pool.MountName == "" {
			pool.MountName = DefaultMountName
		}
		out.Pools = append(out.Pools, pool)
	}
	for _, d := range unassigned {
		out.Unassigned = append(out.Unassigned, toDisk(d))
	}
	s.decorate(&out)
	return out, nil
}

// decorate merges the background readings (disk health, folder sizes).
func (s *Service) decorate(st *StorageStatus) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	apply := func(d *Disk) {
		if h, ok := s.health[d.Path]; ok {
			d.Health = &h
		}
	}
	for i := range st.Pools {
		for j := range st.Pools[i].Disks {
			apply(&st.Pools[i].Disks[j])
		}
	}
	for i := range st.Unassigned {
		apply(&st.Unassigned[i])
	}
	for i := range st.Subvolumes {
		if fs, ok := s.sizes[st.Subvolumes[i].Path]; ok {
			at := fs.MeasuredAt
			st.Subvolumes[i].UsedBytes, st.Subvolumes[i].MeasuredAt = fs.Bytes, &at
		}
	}
}

// ---- background readings ----

// Run keeps disk health and folder sizes fresh until ctx ends. Start it after
// the pools mount: folders are only listed on a mounted pool.
func (s *Service) Run(ctx context.Context) {
	s.loadSizes(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { s.every(ctx, healthInterval, s.checkHealth) })
	wg.Go(func() { s.every(ctx, time.Hour, func(ctx context.Context) { s.measureStale(ctx, sizeInterval) }) })
	wg.Wait()
}

func (s *Service) every(ctx context.Context, d time.Duration, fn func(context.Context)) {
	fn(ctx)
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

// checkHealth reads SMART on every pool and spare disk. A disk that is
// asleep keeps its last reading.
func (s *Service) checkHealth(ctx context.Context) {
	st, err := s.Status(ctx)
	if err != nil {
		return
	}
	var paths []string
	for _, p := range st.Pools {
		for _, d := range p.Disks {
			paths = append(paths, d.Path)
		}
	}
	for _, d := range st.Unassigned {
		paths = append(paths, d.Path)
	}
	changed := false
	for _, path := range paths {
		if path == "" {
			continue
		}
		h, err := s.bc.DiskHealth(ctx, path)
		if err != nil {
			if !errors.Is(err, bcachefs.ErrNoReading) {
				s.log.Warn("SMART check failed", "device", path, "error", err)
			}
			continue
		}
		s.dataMu.Lock()
		s.health[path] = DiskHealth{
			Passed: h.Passed, TemperatureC: h.TemperatureC, PowerOnHours: h.PowerOnHours,
			BadSectors: h.BadSectors, CheckedAt: s.now(),
		}
		s.dataMu.Unlock()
		changed = true
	}
	if changed {
		s.invalidate()
	}
}

// measureStale measures every folder whose size is missing or older than maxAge.
func (s *Service) measureStale(ctx context.Context, maxAge time.Duration) {
	st, err := s.Status(ctx)
	if err != nil {
		return
	}
	var stale []string
	s.dataMu.Lock()
	for _, sv := range st.Subvolumes {
		if fs, ok := s.sizes[sv.Path]; !ok || s.now().Sub(fs.MeasuredAt) >= maxAge {
			stale = append(stale, sv.Path)
		}
	}
	s.dataMu.Unlock()
	if len(stale) > 0 {
		s.SubvolumeUsage(ctx, stale)
	}
}

func (s *Service) loadSizes(ctx context.Context) {
	if s.meta == nil {
		return
	}
	v, ok, err := s.meta.Meta(ctx, sizesKey)
	if err != nil || !ok {
		return
	}
	var sizes map[string]folderSize
	if err := json.Unmarshal([]byte(v), &sizes); err != nil {
		s.log.Warn("stored folder sizes unreadable", "error", err)
		return
	}
	s.dataMu.Lock()
	for k, v := range sizes {
		s.sizes[k] = v
	}
	s.dataMu.Unlock()
	s.invalidate()
}

// saveSizesLocked stores the measured sizes; call with dataMu held.
func (s *Service) saveSizesLocked(ctx context.Context) {
	if s.meta == nil {
		return
	}
	b, _ := json.Marshal(s.sizes)
	if err := s.meta.SetMeta(ctx, sizesKey, string(b)); err != nil {
		s.log.Warn("failed to store folder sizes", "error", err)
	}
}

func toDisk(d bcachefs.Disk) Disk {
	out := Disk{
		Name: d.Name, Path: d.Path, Size: int64(d.Size), Type: d.Type, Model: d.Model, Serial: d.Serial,
		Fstype: d.FSType, Mountpoint: d.Mountpoint, Label: d.Label, HasChildren: d.HasChildren,
		BcachefsLabel: d.BcachefsLabel, DataTarget: d.DataTarget, MetadataTarget: d.MetadataTarget,
		FriendlyName: d.FriendlyName,
	}
	if d.IO != nil {
		out.Io = &DiskIOStats{
			DeviceName: d.IO.DeviceName, ReadIos: int64(d.IO.ReadIOs), WriteIos: int64(d.IO.WriteIOs),
			ReadSectors: int64(d.IO.ReadSectors), WriteSectors: int64(d.IO.WriteSectors),
			IoInProgress: int64(d.IO.IOInProgress),
		}
	}
	return out
}

func toPool(p bcachefs.Pool, autoMount bool) Pool {
	out := Pool{
		Uuid: p.UUID, Name: p.Name, State: PoolState(p.State), Mountdir: p.Mountdir, Label: p.Label,
		DataReplicas: p.DataReplicas, MetadataReplicas: p.MetadataReplicas,
		ForegroundTarget: p.ForegroundTarget, BackgroundTarget: p.BackgroundTarget,
		PromoteTarget: p.PromoteTarget, MetadataTarget: p.MetadataTarget,
		AutoMount: autoMount, Disks: make([]Disk, 0, len(p.Disks)),
	}
	for _, d := range p.Disks {
		out.Disks = append(out.Disks, toDisk(d))
	}
	if p.Usage != nil {
		out.Usage = &PoolUsage{
			TotalBytes: int64(p.Usage.TotalBytes), UsedBytes: int64(p.Usage.UsedBytes),
			AvailableBytes: int64(p.Usage.AvailableBytes), UsedPercent: p.Usage.UsedPercent,
		}
	}
	return out
}

// ---- mount / unmount ----

// Pool auto-mount follows the last explicit mount/unmount: mounting a pool
// records auto_mount=true for it, unmounting records false. A reboot,
// poweroff or power loss doesn't touch it, so AutoMount at startup re-mounts
// exactly the pools that were mounted before.

// Mount mounts a pool from its devices at MountRoot/<name> and remembers it
// for auto-mount. An empty name is the one the pool was last mounted under,
// or DefaultMountName.
func (s *Service) Mount(ctx context.Context, devices []string, name string) error {
	pools, err := s.bc.DiscoverPools(ctx)
	if err != nil {
		s.log.Warn("mount: pool discovery failed, the name is not checked against other pools", "error", err)
	}
	uuid := ""
	for _, p := range pools {
		for _, d := range p.Disks {
			if slices.Contains(devices, d.Path) {
				uuid = p.UUID
			}
		}
	}
	snap := s.cfg.Current()
	if name == "" {
		if pc, ok := snap.Pool(uuid); ok && uuid != "" {
			name, _ = MountName(pc.Mountpoint)
		}
		if name == "" {
			name = DefaultMountName
		}
	}
	if !mountNameRE.MatchString(name) {
		return ErrMountName
	}
	mountpoint := Mountpoint(name)
	for _, p := range snap.Pools {
		if p.UUID != uuid && p.Mountpoint == mountpoint {
			return fmt.Errorf("%w: %s", ErrNameTaken, name)
		}
	}
	for _, p := range pools {
		if p.UUID != uuid && p.Mountdir == mountpoint {
			return fmt.Errorf("%w: %s", ErrNameTaken, name)
		}
	}
	s.invalidate()
	if err := s.open(ctx, mountpoint); err != nil {
		return err
	}
	if err := s.bc.Mount(ctx, devices, mountpoint); err != nil {
		s.guard(ctx, mountpoint)
		return err
	}
	s.rememberMount(ctx, mountpoint)
	// A status read while the mount ran has cached the pool as unmounted.
	s.invalidate()
	return nil
}

// open makes a mountpoint's directory and lifts the flag guard put on it.
func (s *Service) open(ctx context.Context, mountpoint string) error {
	if err := s.mkdir(mountpoint); err != nil {
		return fmt.Errorf("make %s: %w", mountpoint, err)
	}
	if err := s.bc.ClearImmutable(ctx, mountpoint); err != nil {
		s.log.Debug("chattr -i failed (directory may not be immutable)", "mountpoint", mountpoint, "error", err)
	}
	return nil
}

// guard makes the empty directory of a pool that is not mounted immutable.
// It is on /var: without the flag, whatever writes to the pool's paths would
// fill the system disk instead.
func (s *Service) guard(ctx context.Context, mountpoint string) {
	if _, ok := MountName(mountpoint); !ok {
		return
	}
	if err := s.bc.SetImmutable(ctx, mountpoint); err != nil {
		s.log.Warn("chattr +i failed: the unmounted directory can be written to", "mountpoint", mountpoint, "error", err)
	}
}

// rememberMount stores auto_mount=true for the pool now mounted at
// mountpoint. Failures are logged, not returned: the mount itself succeeded.
func (s *Service) rememberMount(ctx context.Context, mountpoint string) {
	pools, err := s.bc.DiscoverPools(ctx)
	if err != nil {
		s.log.Error("auto-mount: pool discovery failed, mount not remembered", "mountpoint", mountpoint, "error", err)
		return
	}
	for _, p := range pools {
		if p.State == "mounted" && p.Mountdir == mountpoint {
			if _, err := s.cfg.SetPoolAutoMount(ctx, p.UUID, mountpoint, true); err != nil {
				s.log.Error("auto-mount: failed to store pool", "mountpoint", mountpoint, "error", err)
				return
			}
			s.log.Info("auto-mount state saved", "mountpoint", mountpoint, "auto_mount", true)
			return
		}
	}
	s.log.Warn("auto-mount: no mounted pool found at mountpoint, mount not remembered", "mountpoint", mountpoint)
}

// Unmount unmounts a pool and records auto_mount=false for it.
func (s *Service) Unmount(ctx context.Context, mountpoint string) error {
	snap := s.cfg.Current()
	s.invalidate()
	if err := s.bc.Unmount(ctx, mountpoint); err != nil {
		return err
	}
	s.guard(ctx, mountpoint)
	// As after a mount: a read during the unmount cached the pool as mounted.
	defer s.invalidate()
	for _, p := range snap.Pools {
		if p.Mountpoint == mountpoint {
			if _, err := s.cfg.SetPoolAutoMount(ctx, p.UUID, mountpoint, false); err != nil {
				s.log.Error("auto-mount: failed to store pool", "mountpoint", mountpoint, "error", err)
			} else {
				s.log.Info("auto-mount state saved", "mountpoint", mountpoint, "auto_mount", false)
			}
			break
		}
	}
	return nil
}

// SetAutoMount changes whether a stored pool is mounted at boot.
func (s *Service) SetAutoMount(ctx context.Context, uuid string, auto bool) error {
	if _, ok := s.cfg.Current().Pool(uuid); !ok {
		return fmt.Errorf("%w: %q", ErrUnknownPool, uuid)
	}
	_, err := s.cfg.SetPoolAutoMount(ctx, uuid, "", auto)
	s.invalidate()
	return err
}

// AutoMount mounts every pool stored with auto_mount at boot. On a cold boot
// the disks can still be enumerating when the daemon starts, so a pool that is
// not discovered yet is retried for up to the discovery timeout (2 minutes).
func (s *Service) AutoMount(ctx context.Context) {
	pending := make(map[string]config.Pool)
	for _, p := range s.cfg.Current().Pools {
		if !p.AutoMount {
			continue
		}
		if _, ok := MountName(p.Mountpoint); !ok {
			s.log.Error("pool not mounted: it is remembered outside "+MountRoot+"; mount it again under a name", "uuid", p.UUID, "mountpoint", p.Mountpoint)
			continue
		}
		pending[p.UUID] = p
	}
	if len(pending) == 0 {
		return
	}
	deadline := s.now().Add(s.discoverTimeout)
	for {
		pools, err := s.bc.DiscoverPools(ctx)
		if err != nil {
			s.log.Error("failed to discover bcachefs pools during boot", "error", err)
		}
		for _, p := range pools {
			pc, ok := pending[p.UUID]
			if !ok {
				continue
			}
			delete(pending, p.UUID)
			if p.State == "mounted" && p.Mountdir == pc.Mountpoint {
				s.log.Info("pool already mounted", "uuid", pc.UUID, "mountpoint", pc.Mountpoint)
				continue
			}
			var devices []string
			for _, d := range p.Disks {
				devices = append(devices, d.Path)
			}
			s.log.Info("auto-mounting pool", "uuid", pc.UUID, "mountpoint", pc.Mountpoint)
			if err := s.open(ctx, pc.Mountpoint); err != nil {
				s.log.Error("failed to auto-mount pool", "uuid", pc.UUID, "error", err)
				continue
			}
			if err := s.bc.Mount(ctx, devices, pc.Mountpoint); err != nil {
				s.log.Error("failed to auto-mount pool", "uuid", pc.UUID, "error", err)
				s.guard(ctx, pc.Mountpoint)
			}
		}
		if len(pending) == 0 {
			return
		}
		if s.now().After(deadline) {
			for uuid, p := range pending {
				s.log.Error("auto-mount pool not found", "uuid", uuid)
				// Its directory must not take the writes meant for it.
				if err := s.mkdir(p.Mountpoint); err == nil {
					s.guard(ctx, p.Mountpoint)
				}
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.retryInterval):
		}
	}
}

// ---- subvolumes ----

// SubvolumeUsage returns bytes used per path; -1 where it could not be computed.
// Measured sizes are kept (and stored) for the status view.
func (s *Service) SubvolumeUsage(ctx context.Context, paths []string) map[string]int64 {
	s.measureMu.Lock()
	defer s.measureMu.Unlock()
	sizes, err := s.bc.FolderSizes(ctx, paths)
	if err != nil {
		s.log.Error("failed to measure folder sizes", "error", err)
	}
	s.shrinkBtreeCache()
	out := make(map[string]int64, len(paths))
	measured := map[string]folderSize{}
	for _, p := range paths {
		n, ok := sizes[filepath.Clean(p)]
		if !ok {
			out[p] = -1
			continue
		}
		out[p] = n
		measured[p] = folderSize{Bytes: n, MeasuredAt: s.now()}
	}
	if len(measured) > 0 {
		s.dataMu.Lock()
		for k, v := range measured {
			s.sizes[k] = v
		}
		s.saveSizesLocked(context.WithoutCancel(ctx))
		s.dataMu.Unlock()
		s.invalidate()
	}
	return out
}

// shrinkBtreeCache frees what a bcdu run left in the kernel's btree cache.
func (s *Service) shrinkBtreeCache() {
	if err := s.bc.ShrinkBtreeCache(); err != nil {
		s.log.Warn("failed to shrink the bcachefs btree cache after measuring", "error", err)
	}
}

// CreateSubvolume creates a subvolume at path.
func (s *Service) CreateSubvolume(ctx context.Context, path string) error {
	s.invalidate()
	return s.bc.CreateSubvolume(ctx, path)
}

// DeleteSubvolume deletes an empty subvolume (ErrSubvolumeNotEmpty otherwise).
func (s *Service) DeleteSubvolume(ctx context.Context, path string) error {
	s.invalidate()
	if err := s.bc.DeleteSubvolume(ctx, path); err != nil {
		return err
	}
	s.dataMu.Lock()
	if _, ok := s.sizes[path]; ok {
		delete(s.sizes, path)
		s.saveSizesLocked(ctx)
	}
	s.dataMu.Unlock()
	return nil
}
