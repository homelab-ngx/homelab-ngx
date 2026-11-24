package audit

import (
	"context"

	"homelab-ngx.com/core/internal/tilestatus"
)

// Tiles reports the security tile: the last audit's posture as a tone, or
// its result while it has none, or off when no audit has run.
func (s *Service) Tiles(ctx context.Context) (map[string]tilestatus.TileStatus, error) {
	items, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]tilestatus.TileStatus{"security": tile(items)}, nil
}

var (
	postureTone = map[Posture]tilestatus.TileState{PostureGood: tilestatus.OK, PostureFair: tilestatus.Info, PostureWeak: tilestatus.Warn, PosturePoor: tilestatus.Bad}
	resultTone  = map[Result]tilestatus.TileState{ResultRunning: tilestatus.Info, ResultDone: tilestatus.OK, ResultPartial: tilestatus.Warn, ResultFailed: tilestatus.Bad}
)

func tile(items []SecurityAuditItem) tilestatus.TileStatus {
	if len(items) == 0 {
		return tilestatus.Of(tilestatus.Off)
	}
	if st, ok := postureTone[items[0].Posture]; ok {
		return tilestatus.Of(st)
	}
	return tilestatus.Of(resultTone[items[0].Result])
}
