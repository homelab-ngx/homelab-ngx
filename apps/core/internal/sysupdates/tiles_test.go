package sysupdates

import (
	"context"
	"testing"

	"homelab-ngx.com/core/internal/tilestatus"
)

func TestTile(t *testing.T) {
	for _, c := range []struct {
		name string
		st   ImageStatus
		want tilestatus.TileState
	}{
		{"up to date", ImageStatus{Booted: "3"}, tilestatus.None},
		{"reboot to finish", ImageStatus{PendingReboot: true}, tilestatus.Info},
		{"image ready", ImageStatus{Available: []string{"4"}}, tilestatus.Info},
		{"core ready", ImageStatus{Core: CoreExtension{Available: []string{"0.5.0"}}}, tilestatus.Info},
		{"image fell back", ImageStatus{FellBackFrom: "4", Available: []string{"5"}}, tilestatus.Bad},
		{"core fell back", ImageStatus{Core: CoreExtension{FellBackFrom: "0.5.0"}}, tilestatus.Bad},
		{"boot check", ImageStatus{BootCheck: "stacks did not start"}, tilestatus.Bad},
	} {
		if got := tile(c.st); got.State != c.want || got.Count != nil {
			t.Errorf("%s: got %+v, want %q", c.name, got, c.want)
		}
	}
}

func TestTilesNotImageHost(t *testing.T) {
	got, err := (&Module{}).Tiles(context.Background())
	if err != nil || got != nil {
		t.Fatalf("got %+v, %v; want no badge and no error", got, err)
	}
}
