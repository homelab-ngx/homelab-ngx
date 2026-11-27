package sysupdates

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	changelogFile         = "release-changelog.json"
	changelogFetchTimeout = 5 * time.Second
	changelogRetryEvery   = 5 * time.Minute
	maxChangelogReleases  = 20
	maxChangelogItems     = 12
	maxChangeTextLen      = 240
	maxChangeAreaLen      = 32
)

var semverReleaseRE = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// ReleaseChange is one user-facing change inside a release changelog.
type ReleaseChange struct {
	Area string `json:"area"`
	Text string `json:"text"`
}

// ReleaseNote is the short changelog published for one release.
type ReleaseNote struct {
	Version string          `json:"version"`
	Date    string          `json:"date,omitempty"`
	Changes []ReleaseChange `json:"changes"`
	More    int             `json:"more,omitempty" doc:"Additional user-facing changes omitted from the short list."`
	Url     string          `json:"url,omitempty" doc:"GitHub Release notes URL."`
}

// ReleaseChangelog lists recent releases' short changelogs, newest first.
type ReleaseChangelog struct {
	Releases []ReleaseNote `json:"releases"`
}

type changelogCache struct {
	mu        sync.Mutex
	loaded    bool
	data      ReleaseChangelog
	lastFetch time.Time
}

func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	var n int
	for i := range s {
		if n == max {
			return strings.TrimSpace(s[:i])
		}
		n++
	}
	return s
}

func parseChangelog(b []byte) (ReleaseChangelog, error) {
	var raw ReleaseChangelog
	if err := json.Unmarshal(b, &raw); err != nil {
		return ReleaseChangelog{Releases: []ReleaseNote{}}, err
	}
	seen := make(map[string]bool, len(raw.Releases))
	releases := make([]ReleaseNote, 0, min(len(raw.Releases), maxChangelogReleases))
	for _, r := range raw.Releases {
		v := strings.TrimSpace(r.Version)
		if !releaseVersionRE.MatchString(v) || seen[v] {
			continue
		}
		seen[v] = true
		changes := make([]ReleaseChange, 0, min(len(r.Changes), maxChangelogItems))
		dropped := 0
		for _, c := range r.Changes {
			text := truncateRunes(c.Text, maxChangeTextLen)
			if text == "" {
				continue
			}
			if len(changes) >= maxChangelogItems {
				dropped++
				continue
			}
			area := truncateRunes(strings.ToLower(c.Area), maxChangeAreaLen)
			if area == "" {
				area = "other"
			}
			changes = append(changes, ReleaseChange{Area: area, Text: text})
		}
		more := max(0, r.More) + dropped
		u := strings.TrimSpace(r.Url)
		if !strings.HasPrefix(u, "https://") {
			u = ""
		}
		releases = append(releases, ReleaseNote{
			Version: v,
			Date:    truncateRunes(r.Date, 32),
			Changes: changes,
			More:    more,
			Url:     u,
		})
	}
	slices.SortFunc(releases, func(a, b ReleaseNote) int { return compareVersions(b.Version, a.Version) })
	if len(releases) > maxChangelogReleases {
		releases = releases[:maxChangelogReleases]
	}
	return ReleaseChangelog{Releases: releases}, nil
}

func cloneChangelog(cl ReleaseChangelog) ReleaseChangelog {
	if cl.Releases == nil {
		return ReleaseChangelog{Releases: []ReleaseNote{}}
	}
	out := ReleaseChangelog{Releases: make([]ReleaseNote, len(cl.Releases))}
	for i, r := range cl.Releases {
		out.Releases[i] = r
		if r.Changes == nil {
			out.Releases[i].Changes = []ReleaseChange{}
		} else {
			out.Releases[i].Changes = slices.Clone(r.Changes)
		}
	}
	return out
}

func fetchReleaseChangelog(ctx context.Context, base string, timeout time.Duration) (ReleaseChangelog, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	b, err := getSmall(ctx, base+"/changelog.json?t="+strconv.FormatInt(time.Now().Unix(), 10))
	if err != nil {
		return ReleaseChangelog{Releases: []ReleaseNote{}}, err
	}
	return parseChangelog(b)
}

func (m *Module) changelogPath() string { return filepath.Join(m.dir, changelogFile) }

// loadChangelogLocked reads the cached changelog once; caller holds m.cl.mu.
func (m *Module) loadChangelogLocked() ReleaseChangelog {
	if !m.cl.loaded {
		m.cl.loaded = true
		m.cl.data = ReleaseChangelog{Releases: []ReleaseNote{}}
		if m.dir != "" {
			if b, err := os.ReadFile(m.changelogPath()); err == nil {
				if parsed, perr := parseChangelog(b); perr == nil {
					m.cl.data = parsed
				} else {
					log.Warn("could not read the release changelog cache", "error", perr)
				}
			}
		}
	}
	return m.cl.data
}

func (m *Module) saveChangelogLocked(cl ReleaseChangelog) {
	cl = cloneChangelog(cl)
	m.cl.loaded = true
	m.cl.data = cl
	if m.dir == "" {
		return
	}
	b, err := json.Marshal(cl)
	if err == nil {
		tmp := m.changelogPath() + ".tmp"
		if err = os.WriteFile(tmp, b, 0o640); err == nil {
			err = os.Rename(tmp, m.changelogPath())
		}
	}
	if err != nil {
		log.Warn("could not save the release changelog cache", "error", err)
	}
}

func (m *Module) refreshChangelog(ctx context.Context, base string, timeout time.Duration) {
	m.cl.mu.Lock()
	_ = m.loadChangelogLocked()
	m.cl.lastFetch = m.now()
	m.cl.mu.Unlock()

	cl, err := fetchReleaseChangelog(ctx, base, timeout)
	if err != nil {
		log.Warn("release changelog fetch failed", "error", err)
		return
	}
	m.cl.mu.Lock()
	m.saveChangelogLocked(cl)
	m.cl.mu.Unlock()
}

func hasVersion(cl ReleaseChangelog, v string) bool {
	if v == "" || !semverReleaseRE.MatchString(v) {
		return true
	}
	for _, r := range cl.Releases {
		if r.Version == v {
			return true
		}
	}
	return false
}

// Changelog returns the cached release changelogs, lazily fetching
// /changelog.json when the cache lacks the running or newest release.
func (m *Module) Changelog(ctx context.Context) ReleaseChangelog {
	if !m.image {
		return ReleaseChangelog{Releases: []ReleaseNote{}}
	}
	latest := m.Release().Latest
	booted := bootedVersion()
	core := activeCore()

	m.cl.mu.Lock()
	cl := m.loadChangelogLocked()
	missing := len(cl.Releases) == 0 || !hasVersion(cl, booted) || !hasVersion(cl, core) || !hasVersion(cl, latest)
	canFetch := m.cl.lastFetch.IsZero() || m.now().Sub(m.cl.lastFetch) >= changelogRetryEvery
	m.cl.mu.Unlock()

	if missing && canFetch {
		m.refreshChangelog(ctx, releaseURL(), changelogFetchTimeout)
		m.cl.mu.Lock()
		cl = m.loadChangelogLocked()
		m.cl.mu.Unlock()
	}
	return cloneChangelog(cl)
}
