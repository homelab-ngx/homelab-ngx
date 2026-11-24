package notifications

import (
	"context"

	"homelab-ngx.com/core/internal/tilestatus"
)

// Tiles reports the notifications tile: bad with the count of failing
// channels, else ok with the count of enabled ones, else off.
func Tiles(n *Notifier, c *Channels) tilestatus.Reporter {
	return func(ctx context.Context) (map[string]tilestatus.TileStatus, error) {
		snap, err := n.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		chs, err := c.List(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]tilestatus.TileStatus{"notifications": tile(len(snap.FailingChannels), chs)}, nil
	}
}

func tile(failing int, chs []Channel) tilestatus.TileStatus {
	if failing > 0 {
		return tilestatus.Counted(tilestatus.Bad, failing)
	}
	on := 0
	for _, ch := range chs {
		if ch.Enabled {
			on++
		}
	}
	if on == 0 {
		return tilestatus.Of(tilestatus.Off)
	}
	return tilestatus.Counted(tilestatus.OK, on)
}
