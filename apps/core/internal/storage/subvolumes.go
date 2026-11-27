package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"homelab-ngx.com/core/internal/storage/bcachefs"
)

// Placement profiles for subvolumes on a tiered pool (fast SSD + slow HDD).
const (
	ProfileHot    = "hot"
	ProfileCached = "cached"
	ProfileCold   = "cold"
	ProfileCustom = "custom"
)

var (
	// ErrUnknownSubvolume means the path does not match a subvolume on any mounted pool.
	ErrUnknownSubvolume = errors.New("no such subvolume")
	// ErrNotTiered means a placement profile was requested on a pool without SSD and HDD tiers.
	ErrNotTiered = errors.New("this pool does not have separate SSD and HDD tiers")
	// ErrInvalidProfile means the subvolume profile or copy count request is invalid.
	ErrInvalidProfile = errors.New("invalid subvolume storage profile")
)

func poolTiers(p bcachefs.Pool) (fast, slow string, ok bool) {
	slow = p.BackgroundTarget
	fast = p.ForegroundTarget
	if fast == "" {
		fast = p.PromoteTarget
	}
	ok = fast != "" && slow != "" && fast != slow
	return fast, slow, ok
}

func effectiveTarget(override, poolDefault string) string {
	if override == "none" {
		return ""
	}
	if override != "" {
		return override
	}
	return poolDefault
}

func classifyProfile(p bcachefs.Pool, opts bcachefs.FileOptions) string {
	fast, slow, ok := poolTiers(p)
	if !ok {
		return ""
	}
	fg := effectiveTarget(opts.ForegroundTarget, p.ForegroundTarget)
	bg := effectiveTarget(opts.BackgroundTarget, p.BackgroundTarget)
	pr := effectiveTarget(opts.PromoteTarget, p.PromoteTarget)
	switch {
	case fg == fast && bg == fast && (pr == fast || pr == ""):
		return ProfileHot
	case fg == fast && bg == slow && (pr == fast || (pr == "" && p.PromoteTarget == "")):
		return ProfileCached
	case fg == slow && bg == slow && pr == "":
		return ProfileCold
	default:
		return ProfileCustom
	}
}

// UpdateSubvolume changes placement targets and/or copy count on an existing subvolume.
func (s *Service) UpdateSubvolume(ctx context.Context, req SubvolumeProfileRequest) error {
	if req.Profile == "" && req.Copies == nil {
		return fmt.Errorf("%w: choose a placement profile or copy count", ErrInvalidProfile)
	}
	switch req.Profile {
	case "", ProfileHot, ProfileCached, ProfileCold:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidProfile, req.Profile)
	}
	if req.Copies != nil && (*req.Copies < 0 || *req.Copies > MaxCopies) {
		return fmt.Errorf("%w: copies must be 0..%d", ErrInvalidProfile, MaxCopies)
	}
	if err := s.checkTools(ctx); err != nil {
		return err
	}

	target := filepath.Clean(Canonical(req.Path))
	pools, err := s.bc.DiscoverPools(ctx)
	if err != nil {
		return err
	}
	var owner *bcachefs.Pool
	for i := range pools {
		p := &pools[i]
		if p.State != "mounted" || p.Mountdir == "" {
			continue
		}
		mp := filepath.Clean(p.Mountdir)
		if strings.HasPrefix(target, mp+"/") {
			owner = p
			break
		}
	}
	if owner == nil {
		return fmt.Errorf("%w: %s", ErrUnknownSubvolume, target)
	}
	subs, err := s.bc.ListSubvolumes(ctx, owner.Mountdir)
	if err != nil {
		return err
	}
	found := false
	for _, sv := range subs {
		if filepath.Clean(sv.Path) == target {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrUnknownSubvolume, target)
	}

	var ch bcachefs.FileOptionChanges
	if req.Profile != "" {
		fast, slow, ok := poolTiers(*owner)
		if !ok {
			return ErrNotTiered
		}
		switch req.Profile {
		case ProfileHot:
			ch.ForegroundTarget = &fast
			ch.BackgroundTarget = &fast
			ch.PromoteTarget = &fast
		case ProfileCached:
			if owner.ForegroundTarget == fast && owner.BackgroundTarget == slow && owner.PromoteTarget == fast {
				clearOpt := ""
				ch.ForegroundTarget = &clearOpt
				ch.BackgroundTarget = &clearOpt
				ch.PromoteTarget = &clearOpt
			} else {
				ch.ForegroundTarget = &fast
				ch.BackgroundTarget = &slow
				ch.PromoteTarget = &fast
			}
		case ProfileCold:
			none := "none"
			ch.ForegroundTarget = &slow
			ch.BackgroundTarget = &slow
			ch.PromoteTarget = &none
		}
	}
	if req.Copies != nil {
		if *req.Copies > len(owner.Disks) {
			return fmt.Errorf("%w: the pool has %d disks", ErrTooFewDisks, len(owner.Disks))
		}
		replicas := *req.Copies
		poolCopies := max(owner.DataReplicas, 1)
		if replicas == poolCopies {
			replicas = 0
		}
		ch.DataReplicas = &replicas
	}

	if err := s.bc.SetFileOptions(ctx, target, ch); err != nil {
		return err
	}
	s.invalidate()
	return nil
}
