package powerprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"homelab-ngx.com/core/internal/store"
)

var (
	// ErrInvalid is a setting the host cannot take.
	ErrInvalid = errors.New("invalid setting")
	// ErrManaged: the host is not on the image, so the daemon writes nothing.
	ErrManaged = errors.New("this host's power settings come from its NixOS configuration")
)

const (
	section = "power"
	// sampleEvery is the idle window; sweepEvery re-applies the profile, which
	// catches a USB device plugged in since.
	sampleEvery = time.Minute
	sweepEvery  = 5 * time.Minute
)

// Service keeps the power profile applied and reports the host's idle.
type Service struct {
	paths Paths
	st    *store.Store
	image bool
	log   *slog.Logger
	now   func() time.Time

	mu       sync.Mutex
	ap       *applier
	loaded   bool
	settings PowerProfileSettings
	report   []PowerTunable
	// last is the newest minute sample, prev the one before.
	last, prev sample
}

// New builds the service. image is whether the host runs the image; on any
// other host nothing is written.
func New(p Paths, st *store.Store, image bool, log *slog.Logger) *Service {
	return &Service{
		paths: p, st: st, image: image, log: log, now: time.Now,
		ap: newApplier(p), settings: PowerProfileSettings{Profile: Balanced},
	}
}

// Run applies the saved profile, then samples the idle counters every
// minute and re-applies the profile every five, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	if err := s.load(ctx); err != nil {
		s.log.Warn("power profile unreadable, using balanced", "error", err)
	}
	s.applyLocked()
	s.last = s.paths.sample(s.now())
	s.mu.Unlock()

	tick := time.NewTicker(sampleEvery)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		n++
		s.mu.Lock()
		s.prev, s.last = s.last, s.paths.sample(s.now())
		if n%int(sweepEvery/sampleEvery) == 0 {
			s.applyLocked()
		}
		s.mu.Unlock()
	}
}

// load reads the saved settings once.
func (s *Service) load(ctx context.Context) error {
	if s.loaded {
		return nil
	}
	rs, err := s.st.Queries().ListHostSettings(ctx)
	if err != nil {
		return err
	}
	s.loaded = true
	for _, r := range rs {
		if r.Section != section {
			continue
		}
		var v PowerProfileSettings
		if err := json.Unmarshal([]byte(r.Value), &v); err != nil {
			return err
		}
		if v.Profile.valid() {
			s.settings = v
		}
	}
	return nil
}

// applyLocked writes the profile on an image host and logs what the kernel
// refused.
func (s *Service) applyLocked() {
	if !s.image {
		return
	}
	s.report = s.ap.apply(s.paths.plan(s.settings))
	for _, t := range s.report {
		if t.Refused > 0 {
			s.log.Debug("power tunable refused", "tunable", t.Name, "value", t.Value, "refused", t.Refused, "files", t.Files, "detail", t.Detail)
		}
	}
}

// Status is the profile, what it did, the idle of the last minute and the
// blockers.
func (s *Service) Status(ctx context.Context) (PowerProfileStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx); err != nil {
		return PowerProfileStatus{}, err
	}
	now := s.paths.sample(s.now())
	// A sample from a moment ago makes a window too short to read: use the
	// one before it. With neither, the window is the time since boot.
	base := s.last
	if now.at.Sub(base.at) < 5*time.Second {
		base = s.prev
	}
	report := s.report
	if report == nil {
		report = []PowerTunable{}
	}
	return PowerProfileStatus{
		PowerProfileSettings: s.settings, ReadOnly: !s.image,
		Driver: s.paths.driver(), Governors: s.paths.governors(),
		Tunables: report, Idle: idle(base, now), Blockers: s.paths.blockers(),
	}, nil
}

// Set saves and applies a profile and governor.
func (s *Service) Set(ctx context.Context, in PowerProfileSettings) (PowerProfileStatus, error) {
	if !s.image {
		return PowerProfileStatus{}, ErrManaged
	}
	if !in.Profile.valid() {
		return PowerProfileStatus{}, fmt.Errorf("%w: profile: power-saver, balanced or performance", ErrInvalid)
	}
	if in.Governor != "" {
		govs := s.paths.governors()
		if len(govs) == 0 {
			return PowerProfileStatus{}, fmt.Errorf("%w: governor: this host has no CPU frequency scaling", ErrInvalid)
		}
		if !slices.Contains(govs, in.Governor) {
			return PowerProfileStatus{}, fmt.Errorf("%w: governor: one of %v", ErrInvalid, govs)
		}
	}
	v, err := json.Marshal(in)
	if err != nil {
		return PowerProfileStatus{}, err
	}
	s.mu.Lock()
	err = s.st.Queries().UpsertHostSetting(ctx, store.UpsertHostSettingParams{
		Section: section, Value: string(v), UpdatedAt: s.now().UnixMilli(),
	})
	if err == nil {
		s.loaded, s.settings = true, in
		s.applyLocked()
	}
	s.mu.Unlock()
	if err != nil {
		return PowerProfileStatus{}, err
	}
	return s.Status(ctx)
}
