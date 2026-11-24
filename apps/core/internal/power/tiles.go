package power

import (
	"context"

	"homelab-ngx.com/core/internal/tilestatus"
)

// Tiles reports the power tile: info while a reboot or shutdown is
// scheduled. It asks logind directly: Status would mark a page as following
// a shutdown.
func (s *Service) Tiles(ctx context.Context) (map[string]tilestatus.TileStatus, error) {
	sch, err := s.logind.Scheduled(ctx)
	if err != nil {
		return nil, err
	}
	if sch == nil {
		return nil, nil
	}
	return map[string]tilestatus.TileStatus{"power": tilestatus.Of(tilestatus.Info)}, nil
}
