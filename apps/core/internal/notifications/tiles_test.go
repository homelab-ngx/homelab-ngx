package notifications

import (
	"testing"

	"homelab-ngx.com/core/internal/tilestatus"
)

func TestTile(t *testing.T) {
	chs := []Channel{{Enabled: true}, {Enabled: false}, {Enabled: true}}
	for _, c := range []struct {
		failing int
		chs     []Channel
		state   tilestatus.TileState
		count   int
	}{
		{0, nil, tilestatus.Off, 0},
		{0, []Channel{{Enabled: false}}, tilestatus.Off, 0},
		{0, chs, tilestatus.OK, 2},
		{1, chs, tilestatus.Bad, 1},
	} {
		got := tile(c.failing, c.chs)
		n := 0
		if got.Count != nil {
			n = *got.Count
		}
		if got.State != c.state || n != c.count {
			t.Errorf("failing %d, %d channels: got %+v, want %s %d", c.failing, len(c.chs), got, c.state, c.count)
		}
	}
}
