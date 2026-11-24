package audit

import (
	"testing"

	"homelab-ngx.com/core/internal/tilestatus"
)

func TestTile(t *testing.T) {
	for _, c := range []struct {
		items []SecurityAuditItem
		want  tilestatus.TileState
	}{
		{nil, tilestatus.Off},
		{[]SecurityAuditItem{{Result: ResultDone, Posture: PostureWeak}, {Posture: PostureGood}}, tilestatus.Warn},
		{[]SecurityAuditItem{{Result: ResultDone, Posture: PosturePoor}}, tilestatus.Bad},
		{[]SecurityAuditItem{{Result: ResultRunning}, {Posture: PostureGood}}, tilestatus.Info},
		{[]SecurityAuditItem{{Result: ResultFailed}}, tilestatus.Bad},
	} {
		if got := tile(c.items); got.State != c.want || got.Count != nil {
			t.Errorf("%+v: got %+v, want %s", c.items, got, c.want)
		}
	}
}
