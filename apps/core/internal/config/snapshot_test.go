package config

import (
	"testing"
	"time"
)

func TestBuildSortsPools(t *testing.T) {
	t.Parallel()
	in := []Pool{{UUID: "z"}, {UUID: "a", AutoMount: true}}
	s := build(&Nix{Version: SupportedVersion}, in, 5, time.Unix(0, 0))
	if s.Pools[0].UUID != "a" || s.Pools[1].UUID != "z" || s.Generation != 5 {
		t.Errorf("pools/gen = %+v %d", s.Pools, s.Generation)
	}
	if in[0].UUID != "z" {
		t.Error("build sorted the caller's slice")
	}
	if p, ok := s.Pool("a"); !ok || !p.AutoMount {
		t.Errorf("Pool(a) = %+v %v", p, ok)
	}
	if _, ok := s.Pool("nope"); ok {
		t.Error("found unknown pool")
	}
}
