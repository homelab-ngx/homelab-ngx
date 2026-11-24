package llm

import (
	"context"

	"homelab-ngx.com/core/internal/tilestatus"
)

// Tiles reports the assistant tile: ok when a provider is set, else off.
func (s *Settings) Tiles(ctx context.Context) (map[string]tilestatus.TileStatus, error) {
	a, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	st := tilestatus.OK
	if a.Provider == KindNone {
		st = tilestatus.Off
	}
	return map[string]tilestatus.TileStatus{"assistant": tilestatus.Of(st)}, nil
}
