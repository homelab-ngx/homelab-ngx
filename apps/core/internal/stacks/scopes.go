package stacks

import (
	"context"

	"homelab-ngx.com/core/internal/services"
)

// Measured is a built stack and the service units its manifest's health
// check names, without the disabled ones: what the metrics sampler reads.
type Measured struct {
	Name  string
	Units []string
}

// Measured lists every built stack with its health units.
func (s *Service) Measured(ctx context.Context) []Measured {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return nil
	}
	var out []Measured
	for _, r := range rows {
		g, err := s.generation(ctx, r)
		if err != nil || g == nil {
			continue
		}
		mv := Measured{Name: r.Name}
		if m, err := decode(g.Manifest); err == nil {
			mv.Units = enabled(m.Health.Units, liveMasks(r, m))
		}
		out = append(out, mv)
	}
	return out
}

// SetUsage gives the service the metrics sampler's latest CPU and memory of
// a stack or of "<stack>/<unit>".
func (s *Service) SetUsage(f func(key string) *services.Usage) {
	s.mu.Lock()
	s.usageFn = f
	s.mu.Unlock()
}

// usageOf is the latest CPU and memory of a stack or "<stack>/<unit>"; nil
// when unknown.
func (s *Service) usageOf(key string) *services.Usage {
	s.mu.Lock()
	f := s.usageFn
	s.mu.Unlock()
	if f == nil {
		return nil
	}
	return f(key)
}
