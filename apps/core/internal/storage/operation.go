package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/storage/bcachefs"
)

// The removal of a disk from a pool. The kernel moves the data and keeps the
// member's state in the superblock, so a removal outlives the daemon: what
// was asked for is kept in the meta store and picked up again at start.

// removalsKey is the meta row with the removals that are under way.
const removalsKey = "storage.pool_removal"

// removal is what a running removal needs to go on after a restart.
type removal struct {
	PoolUUID   string    `json:"pool_uuid"`
	Mountpoint string    `json:"mountpoint"`
	Disk       string    `json:"disk"` // the device path when it started
	Name       string    `json:"name"` // sdc
	Serial     string    `json:"serial"`
	Total      uint64    `json:"total"`
	StartedAt  time.Time `json:"started_at"`
}

// poolOp is a removal that runs or last ran. opMu guards view.
type poolOp struct {
	rec    removal
	view   PoolOperation
	cancel context.CancelFunc
	// stopped is set by Cancel: the tool's end is then not a failure.
	stopped bool
}

// operation is the pool's removal for the status view.
func (s *Service) operation(uuid string) *PoolOperation {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	op, ok := s.ops[uuid]
	if !ok {
		return nil
	}
	v := op.view
	return &v
}

func (s *Service) busy(uuid string) bool {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	op, ok := s.ops[uuid]
	return ok && op.view.FinishedAt == nil
}

// busyAt is busy for the pool mounted at mountpoint.
func (s *Service) busyAt(mountpoint string) bool {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	for _, op := range s.ops {
		if op.rec.Mountpoint == mountpoint && op.view.FinishedAt == nil {
			return true
		}
	}
	return false
}

func (s *Service) forgetOperation(uuid string) {
	s.opMu.Lock()
	delete(s.ops, uuid)
	s.opMu.Unlock()
}

// start runs a removal in the background and remembers it.
func (s *Service) start(ctx context.Context, rec removal) {
	run, cancel := context.WithCancel(s.bg)
	op := &poolOp{rec: rec, cancel: cancel, view: PoolOperation{
		Kind: "remove", Disk: rec.Disk, Phase: "moving", StartedAt: rec.StartedAt,
		TotalBytes: int64(rec.Total), LeftBytes: int64(rec.Total),
	}}
	s.opMu.Lock()
	s.ops[rec.PoolUUID] = op
	s.opMu.Unlock()
	s.saveRemoval(ctx, rec.PoolUUID, &rec)
	go func() {
		defer cancel()
		s.remove(run, op)
	}()
}

// failed shows a removal a restart could not go on with.
func (s *Service) failed(rec removal, err error) {
	now := s.now()
	s.opMu.Lock()
	s.ops[rec.PoolUUID] = &poolOp{rec: rec, view: PoolOperation{
		Kind: "remove", Disk: rec.Disk, Phase: "failed", StartedAt: rec.StartedAt, FinishedAt: &now,
		Error: err.Error(), TotalBytes: int64(rec.Total),
	}}
	s.opMu.Unlock()
	s.invalidate()
	s.notifyRemoval(s.bg, rec, err)
}

func (s *Service) setPhase(op *poolOp, phase string) {
	s.opMu.Lock()
	op.view.Phase = phase
	s.opMu.Unlock()
	s.invalidate()
}

// remove is the removal itself: move the data off, take the disk out of the
// pool, wipe it.
func (s *Service) remove(ctx context.Context, op *poolOp) {
	rec := op.rec
	err := s.evacuate(ctx, op)
	if err == nil {
		s.setPhase(op, "removing")
		err = s.bc.RemoveDevice(ctx, rec.Disk)
	}
	if err == nil {
		s.setPhase(op, "erasing")
		// The removed disk still carries the pool's superblock and would be
		// listed, and mounted, as a member.
		err = s.bc.Wipe(ctx, rec.Disk, nil)
	}

	s.opMu.Lock()
	stopped := op.stopped
	s.opMu.Unlock()
	if s.bg.Err() != nil && !stopped {
		// The daemon is stopping. The kernel goes on moving; the record
		// stays, and the next start picks the removal up.
		return
	}
	clean := context.WithoutCancel(ctx)
	if err != nil {
		// Give the disk back to the pool: an evacuating disk takes no data.
		if serr := s.bc.SetDeviceState(clean, rec.Disk, "rw"); serr != nil {
			s.log.Warn("could not set the disk back to rw", "disk", rec.Disk, "error", serr)
		}
	}
	s.saveRemoval(clean, rec.PoolUUID, nil)
	if stopped {
		s.forgetOperation(rec.PoolUUID)
		s.invalidate()
		return
	}
	now := s.now()
	s.opMu.Lock()
	op.view.FinishedAt = &now
	if err != nil {
		op.view.Phase, op.view.Error = "failed", err.Error()
		s.log.Error("removing a disk from the pool failed", "disk", rec.Disk, "error", err)
	} else {
		op.view.Phase, op.view.LeftBytes = "done", 0
		s.log.Info("disk removed from the pool", "disk", rec.Disk)
	}
	s.opMu.Unlock()
	s.invalidate()
	s.notifyRemoval(clean, rec, err)
}

func (s *Service) notifyRemoval(ctx context.Context, rec removal, err error) {
	if s.notify == nil {
		return
	}
	msg := notifications.Message{
		Severity: notifications.Info,
		Title:    "Disk " + rec.Name + " removed from pool",
		Body:     rec.Disk,
		Path:     "/storage",
		Event:    "storage.disk",
		Key:      "disk-remove:" + rec.Name,
	}
	if err != nil {
		msg.Severity = notifications.Alert
		msg.Title = "Disk " + rec.Name + " removal failed"
		msg.Body = err.Error()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, nerr := s.notify.Send(ctx, msg); nerr != nil && !errors.Is(nerr, notifications.ErrNoChannels) {
		s.log.Warn("storage notification not sent", "disk", rec.Disk, "error", nerr)
	}
}

// evacuate waits for the kernel to move the disk's data away, reading how
// much is left. The tool waits forever when the other disks are full, so no
// progress for stallAfter is a failure.
func (s *Service) evacuate(ctx context.Context, op *poolOp) error {
	rec := op.rec
	tool, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- s.bc.Evacuate(tool, rec.Disk) }()

	tick := time.NewTicker(s.usageEvery)
	defer tick.Stop()
	left, moved := rec.Total, s.now()
	for {
		select {
		case err := <-done:
			return err
		case <-tick.C:
			usage, err := s.bc.DeviceUsage(tool, rec.Mountpoint)
			if err != nil {
				continue
			}
			i := slices.IndexFunc(usage, func(u bcachefs.DeviceUsage) bool { return u.Name == rec.Name })
			if i < 0 {
				continue
			}
			now := usage[i].Leaving
			if now == 0 {
				now = usage[i].Used
			}
			if now != left {
				left, moved = now, s.now()
				s.opMu.Lock()
				op.view.LeftBytes = int64(left)
				s.opMu.Unlock()
				s.invalidate()
			} else if left > 0 && s.now().Sub(moved) >= s.stallAfter {
				stop()
				<-done
				return fmt.Errorf("nothing moved for %s with %d bytes left: the other disks are full", s.stallAfter, left)
			}
		}
	}
}

// CancelRemoval stops a running removal and gives the disk back to the pool;
// what was moved stays where it is. For a removal that ended, it clears it
// from the status.
func (s *Service) CancelRemoval(uuid string) error {
	s.opMu.Lock()
	op, ok := s.ops[uuid]
	if !ok {
		s.opMu.Unlock()
		return fmt.Errorf("%w: no removal on %q", ErrUnknownPool, uuid)
	}
	if op.view.FinishedAt != nil {
		delete(s.ops, uuid)
		s.opMu.Unlock()
		s.invalidate()
		return nil
	}
	if op.view.Phase != "moving" {
		s.opMu.Unlock()
		return errors.New("the disk has left the pool; the removal cannot be stopped now")
	}
	op.stopped = true
	cancel := op.cancel
	s.opMu.Unlock()
	cancel()
	return nil
}

func (s *Service) loadRemovals(ctx context.Context) map[string]removal {
	out := map[string]removal{}
	if s.meta == nil {
		return out
	}
	v, ok, err := s.meta.Meta(ctx, removalsKey)
	if err != nil || !ok || v == "" {
		return out
	}
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		s.log.Warn("stored removals unreadable", "error", err)
	}
	return out
}

// saveRemoval stores rec for the pool, or drops the pool's when rec is nil.
func (s *Service) saveRemoval(ctx context.Context, uuid string, rec *removal) {
	if s.meta == nil {
		return
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	all := s.loadRemovals(ctx)
	if rec == nil {
		delete(all, uuid)
	} else {
		all[uuid] = *rec
	}
	b, _ := json.Marshal(all)
	if err := s.meta.SetMeta(ctx, removalsKey, string(b)); err != nil {
		s.log.Warn("failed to store the removal", "error", err)
	}
}

// ResumeRemovals goes on with the removals a restart interrupted. Call it
// once the pools are mounted. Background work of the service ends with ctx.
func (s *Service) ResumeRemovals(ctx context.Context) {
	s.bg = ctx
	recs := s.loadRemovals(ctx)
	if len(recs) == 0 {
		return
	}
	pools, err := s.bc.DiscoverPools(ctx)
	if err != nil {
		s.log.Error("removals not resumed: pool discovery failed", "error", err)
		return
	}
	for uuid, rec := range recs {
		i := slices.IndexFunc(pools, func(p bcachefs.Pool) bool { return p.UUID == uuid })
		if i < 0 {
			s.log.Warn("a removal's pool is gone", "pool", uuid)
			s.saveRemoval(ctx, uuid, nil)
			continue
		}
		pool := pools[i]
		// The disk can have another path after a reboot: find it by serial.
		j := slices.IndexFunc(pool.Disks, func(d bcachefs.Disk) bool {
			if rec.Serial != "" && d.Serial != "" {
				return d.Serial == rec.Serial
			}
			return d.Path == rec.Disk
		})
		if j < 0 {
			s.log.Warn("a removal's disk is not in the pool any more", "pool", uuid, "disk", rec.Disk)
			s.saveRemoval(ctx, uuid, nil)
			continue
		}
		rec.Disk, rec.Name = pool.Disks[j].Path, pool.Disks[j].Name
		if pool.State != "mounted" {
			s.saveRemoval(ctx, uuid, nil)
			s.failed(rec, errors.New("the pool was not mounted when the system started; mount it and remove the disk again"))
			continue
		}
		rec.Mountpoint = pool.Mountdir
		if !memberOf(ctx, s.bc, pool, rec.Name) {
			// It left the pool and was not wiped yet.
			if err := s.bc.Wipe(ctx, rec.Disk, nil); err != nil {
				s.failed(rec, err)
			}
			s.saveRemoval(ctx, uuid, nil)
			continue
		}
		s.log.Info("going on with the removal of a disk", "pool", uuid, "disk", rec.Disk)
		s.start(ctx, rec)
	}
	s.invalidate()
}

// memberOf reports whether the mounted pool still counts the disk: a removed
// one keeps a superblock that names the pool, but has no usage row.
func memberOf(ctx context.Context, bc *bcachefs.Client, pool bcachefs.Pool, name string) bool {
	usage, err := bc.DeviceUsage(ctx, pool.Mountdir)
	if err != nil {
		return true
	}
	return slices.ContainsFunc(usage, func(u bcachefs.DeviceUsage) bool { return u.Name == name })
}
