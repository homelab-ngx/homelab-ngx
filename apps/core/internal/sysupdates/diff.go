package sysupdates

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	// "name: 1.0, 1.1 → 1.2, +1.2 MiB" — the size suffix is optional.
	diffLineRe = regexp.MustCompile(`^(\S+): (.+?) → (.+?)(?:, [+-]?[0-9.]+ [KMGT]?i?B)?$`)
)

// ParseDiffClosures turns `nix store diff-closures` output into version
// changes. Size-only lines and config derivations with no version (∅ → ε)
// are dropped.
func ParseDiffClosures(out string) []UpdateChange {
	var changes []UpdateChange
	for _, line := range strings.Split(ansiRe.ReplaceAllString(out, ""), "\n") {
		m := diffLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		from, to := versionList(m[2]), versionList(m[3])
		if from == "" && to == "" {
			continue
		}
		changes = append(changes, UpdateChange{
			Name: m[1],
			From: from,
			To:   to,
			Kind: UpdateChangeKind(changeKind(from, to)),
		})
	}
	return changes
}

// versionList normalizes "∅" (absent) and "ε" (no version) out of a
// comma-separated version set.
func versionList(s string) string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		v = strings.TrimSpace(v)
		if v == "" || v == "∅" || v == "ε" {
			continue
		}
		out = append(out, v)
	}
	return strings.Join(out, ", ")
}

func changeKind(from, to string) string {
	switch {
	case from == "" && to == "":
		return "changed"
	case from == "":
		return "added"
	case to == "":
		return "removed"
	}
	last := func(s string) string { p := strings.Split(s, ", "); return p[len(p)-1] }
	switch c := compareVersions(last(from), last(to)); {
	case c < 0:
		return "upgrade"
	case c > 0:
		return "downgrade"
	}
	return "changed"
}

// compareVersions compares dotted/dashed versions segment by segment,
// numerically where both segments are numbers: close enough to systemd's
// order for package versions and for ours (0.0.YYYYMMDDHHMM, then semver).
func compareVersions(a, b string) int {
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '_' || r == '+' })
	}
	as, bs := split(a), split(b)
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		if aerr == nil && berr == nil {
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
			continue
		}
		if c := strings.Compare(as[i], bs[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}
