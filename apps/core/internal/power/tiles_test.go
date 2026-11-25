package power

import (
	"context"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/tilestatus"
)

func TestTiles(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if got, err := r.svc.Tiles(ctx); err != nil || got != nil {
		t.Fatalf("nothing scheduled: %+v, %v", got, err)
	}
	r.logind.sched = &PowerScheduled{Action: Reboot, At: time.Now().Add(time.Hour)}
	if got, err := r.svc.Tiles(ctx); err != nil || got["system"].State != tilestatus.Info {
		t.Fatalf("scheduled: %+v, %v", got, err)
	}
}
