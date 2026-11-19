package sysupdates

import "testing"

func TestParseDiffClosures(t *testing.T) {
	t.Parallel()
	out := "AGENTS.md: ∅ → ε\n" +
		"SDL2_mixer: ∅ → 2.8.2, \x1b[31;1m415.5 KiB\x1b[0m\n" +
		"aardvark-dns: \x1b[31;1m34.5 KiB\x1b[0m\n" +
		"abseil-cpp: 20210324.2, 20260107.1 → 20260817.0, \x1b[32;1m-4.5 MiB\x1b[0m\n" +
		"jellyfin: 12.2 → 12.1\n" +
		"old-thing: 1.0 → ∅, -3.0 KiB\n"
	got := ParseDiffClosures(out)
	want := []UpdateChange{
		{Name: "SDL2_mixer", From: "", To: "2.8.2", Kind: "added"},
		{Name: "abseil-cpp", From: "20210324.2, 20260107.1", To: "20260817.0", Kind: "upgrade"},
		{Name: "jellyfin", From: "12.2", To: "12.1", Kind: "downgrade"},
		{Name: "old-thing", From: "1.0", To: "", Kind: "removed"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d changes, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("change %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCompareVersions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int
	}{
		{"1.9", "1.10", -1},
		{"7.2.9-1.39.6", "7.2.3-1.39.2", 1},
		{"1.0", "1.0", 0},
		{"1.0", "1.0.1", -1},
		// Image versions: builds before the first release are 0.0.<commit
		// time>, so every release sorts after them.
		{"0.0.202611180849", "0.0.202611181200", -1},
		{"0.1.0", "0.0.202611180849", 1},
		{"0.9.0", "0.10.0", -1},
		// A build of main after a release is <release>.<commit time>: above
		// the release it follows, below the next one.
		{"0.1.0.202611190130", "0.1.0", 1},
		{"0.1.0.202611190130", "0.1.1", -1},
		{"0.1.0.202611190130", "0.1.0.202611190200", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
