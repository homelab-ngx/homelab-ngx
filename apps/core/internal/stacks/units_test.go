package stacks

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestUnitNamesCarryTheStackName(t *testing.T) {
	t.Parallel()
	units := []Unit{
		{Stack: "immich", Unit: "immich-server.service"},
		{Stack: "immich", Unit: "postgresql.service"},
		{Stack: "media", Unit: "plex.service"},
		{Stack: "media", Unit: "postgresql.service"},
	}
	qualify(units)
	var got []string
	for _, u := range units {
		got = append(got, u.Name)
	}
	want := []string{"immich-server.service", "immich-postgresql.service", "media-plex.service", "media-postgresql.service"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}

	svc, _ := newService(t)
	svc.units, svc.unitsAt = units, time.Now()
	for name, want := range map[string][2]string{
		"media-postgresql":   {"media", "postgresql.service"},
		"media-plex.service": {"media", "plex.service"},
		"immich-server":      {"immich", "immich-server.service"},
	} {
		stack, unit, ok := svc.UnitStack(context.Background(), name)
		if !ok || stack != want[0] || unit != want[1] {
			t.Errorf("UnitStack(%q) = %q %q %v, want %v", name, stack, unit, ok, want)
		}
	}
	for _, bare := range []string{"postgresql", "plex"} {
		if _, _, ok := svc.UnitStack(context.Background(), bare); ok {
			t.Errorf("%q resolved without its stack name", bare)
		}
	}
}
